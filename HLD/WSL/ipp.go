package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
)

// IPP value tags (RFC 8010)
const (
	tagUnsupported        byte = 0x10
	tagInteger            byte = 0x21
	tagBoolean            byte = 0x22
	tagEnum               byte = 0x23
	tagOctetString        byte = 0x30
	tagTextWithoutLang    byte = 0x41
	tagNameWithoutLang    byte = 0x42
	tagKeyword            byte = 0x44
	tagURI                byte = 0x45
	tagCharset            byte = 0x47
	tagNaturalLanguage    byte = 0x48
	tagMimeMediaType      byte = 0x49
	tagRangeOfInteger     byte = 0x33
	tagResolution         byte = 0x32
)

// IPP delimiter tags
const (
	tagOperationAttributes byte = 0x01
	tagJobAttributes       byte = 0x02
	tagEndOfAttributes     byte = 0x03
	tagPrinterAttributes   byte = 0x04
)

// IPP operation ids
const (
	opPrintJob              uint16 = 0x0002
	opCancelJob             uint16 = 0x0008
	opGetJobAttributes      uint16 = 0x0009
	opGetJobs               uint16 = 0x000A
	opGetPrinterAttributes  uint16 = 0x000B
)

type ippAttribute struct {
	Tag   byte
	Name  string
	Value string
}

type ippResponse struct {
	Version       uint16
	StatusCode    uint16
	RequestID     uint32
	Attributes    []ippAttribute
}

func writeAttribute(buf *bytes.Buffer, tag byte, name, value string) {
	buf.WriteByte(tag)
	binary.Write(buf, binary.BigEndian, uint16(len(name)))
	buf.WriteString(name)
	binary.Write(buf, binary.BigEndian, uint16(len(value)))
	buf.WriteString(value)
}

func writeIntegerAttribute(buf *bytes.Buffer, tag byte, name string, value int32) {
	buf.WriteByte(tag)
	binary.Write(buf, binary.BigEndian, uint16(len(name)))
	buf.WriteString(name)
	binary.Write(buf, binary.BigEndian, uint16(4))
	binary.Write(buf, binary.BigEndian, value)
}

func writeEnumAttribute(buf *bytes.Buffer, name string, value int32) {
	writeIntegerAttribute(buf, tagEnum, name, value)
}

func writeResolutionAttribute(buf *bytes.Buffer, name string, xres, yres int32, dpi bool) {
	buf.WriteByte(tagResolution)
	binary.Write(buf, binary.BigEndian, uint16(len(name)))
	buf.WriteString(name)
	binary.Write(buf, binary.BigEndian, uint16(9))
	binary.Write(buf, binary.BigEndian, xres)
	binary.Write(buf, binary.BigEndian, yres)
	if dpi {
		buf.WriteByte(3)
	} else {
		buf.WriteByte(4)
	}
}

func writeRangeOfIntegerAttribute(buf *bytes.Buffer, name string, lower, upper int32) {
	buf.WriteByte(tagRangeOfInteger)
	binary.Write(buf, binary.BigEndian, uint16(len(name)))
	buf.WriteString(name)
	binary.Write(buf, binary.BigEndian, uint16(8))
	binary.Write(buf, binary.BigEndian, lower)
	binary.Write(buf, binary.BigEndian, upper)
}

// buildRequest encodes the common operation-attributes group shared by
// Print-Job and Get-Printer-Attributes, followed by op-specific extras.
func buildRequest(operation uint16, requestID uint32, printerURI, requestingUser string, extra func(*bytes.Buffer)) *bytes.Buffer {
	buf := &bytes.Buffer{}

	binary.Write(buf, binary.BigEndian, uint16(0x0200)) // IPP version 2.0
	binary.Write(buf, binary.BigEndian, operation)
	binary.Write(buf, binary.BigEndian, requestID)

	buf.WriteByte(tagOperationAttributes)
	writeAttribute(buf, tagCharset, "attributes-charset", "utf-8")
	writeAttribute(buf, tagNaturalLanguage, "attributes-natural-language", "en-us")
	writeAttribute(buf, tagURI, "printer-uri", printerURI)
	writeAttribute(buf, tagNameWithoutLang, "requesting-user-name", requestingUser)

	if extra != nil {
		extra(buf)
	}

	buf.WriteByte(tagEndOfAttributes)
	return buf
}

// sendIPP posts an IPP request (optionally followed by document data) to the
// given HTTP(S) endpoint and parses the response header + attributes.
func sendIPP(endpoint string, request *bytes.Buffer, document io.Reader) (*ippResponse, error) {
	var body io.Reader = request
	if document != nil {
		body = io.MultiReader(request, document)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("build http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/ipp")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http post to %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected HTTP status %s from %s: %s", resp.Status, endpoint, string(data))
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read ipp response body: %w", err)
	}

	return parseResponse(raw)
}

func parseResponse(raw []byte) (*ippResponse, error) {
	if len(raw) < 8 {
		return nil, fmt.Errorf("ipp response too short (%d bytes)", len(raw))
	}

	r := &ippResponse{
		Version:    binary.BigEndian.Uint16(raw[0:2]),
		StatusCode: binary.BigEndian.Uint16(raw[2:4]),
		RequestID:  binary.BigEndian.Uint32(raw[4:8]),
	}

	pos := 8
	var lastName string
	for pos < len(raw) {
		tag := raw[pos]
		pos++
		if tag == tagEndOfAttributes {
			break
		}
		// delimiter tags for attribute groups (operation/job/printer/unsupported) - skip marker
		if tag <= 0x0F {
			continue
		}
		if pos+2 > len(raw) {
			break
		}
		nameLen := int(binary.BigEndian.Uint16(raw[pos : pos+2]))
		pos += 2
		name := string(raw[pos : pos+nameLen])
		pos += nameLen
		if name == "" {
			name = lastName // additional value for a multi-valued attribute
		} else {
			lastName = name
		}

		if pos+2 > len(raw) {
			break
		}
		valueLen := int(binary.BigEndian.Uint16(raw[pos : pos+2]))
		pos += 2
		value := raw[pos : pos+valueLen]
		pos += valueLen

		r.Attributes = append(r.Attributes, ippAttribute{
			Tag:   tag,
			Name:  name,
			Value: decodeValue(tag, value),
		})
	}

	return r, nil
}

func decodeValue(tag byte, raw []byte) string {
	switch tag {
	case tagInteger, tagEnum:
		if len(raw) == 4 {
			return fmt.Sprintf("%d", int32(binary.BigEndian.Uint32(raw)))
		}
	case tagBoolean:
		if len(raw) == 1 {
			if raw[0] == 1 {
				return "true"
			}
			return "false"
		}
	case tagResolution:
		if len(raw) == 9 {
			x := int32(binary.BigEndian.Uint32(raw[0:4]))
			y := int32(binary.BigEndian.Uint32(raw[4:8]))
			unit := "dpi"
			if raw[8] == 4 {
				unit = "dpcm"
			}
			return fmt.Sprintf("%dx%d%s", x, y, unit)
		}
	case tagRangeOfInteger:
		if len(raw) == 8 {
			lo := int32(binary.BigEndian.Uint32(raw[0:4]))
			hi := int32(binary.BigEndian.Uint32(raw[4:8]))
			return fmt.Sprintf("%d-%d", lo, hi)
		}
	}
	return string(raw)
}

func statusName(code uint16) string {
	if code < 0x0100 {
		return "successful-ok"
	}
	return fmt.Sprintf("error-0x%04x", code)
}
