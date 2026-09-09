package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
	"unicode/utf8"
)

// ippClientTimeout is httpClient's default Timeout, overridable via -timeout.
// Without a timeout, a target that accepts the TCP connection but never
// answers blocks the caller forever instead of giving up.
const ippClientTimeout = 60 * time.Second

// httpClient is shared across every sendIPP call so -timeout applies
// uniformly, including to bench's concurrent goroutines. Timeout must be set
// via setClientTimeout before any worker goroutine starts - it is not safe to
// mutate concurrently with a request in flight.
var httpClient = &http.Client{Timeout: ippClientTimeout}

// setClientTimeout applies a -timeout flag value (0 = keep the default) to
// the shared client. Called once per subcommand, before any sendIPP call.
func setClientTimeout(d time.Duration) {
	if d == 0 {
		return
	}
	if d < 0 {
		fmt.Fprintf(os.Stderr, "error: -timeout must be positive, got %s\n", d)
		os.Exit(1)
	}
	httpClient.Timeout = d
}

// maxIPPResponseBytes bounds how much of a response body sendIPP will read.
// Get-Printer-Attributes/Get-Jobs responses are metadata, not documents; a
// misbehaving or malicious endpoint returning something drastically larger
// should be rejected rather than fully buffered into memory.
const maxIPPResponseBytes = 16 << 20 // 16 MiB

// maxIPPFieldLen is the largest name/value length IPP's uint16 length prefix
// can encode; a longer value would silently wrap the prefix while still
// writing the full string, desyncing the rest of the message.
const maxIPPFieldLen = 65535

// ippSafeString clamps s to at most maxIPPFieldLen bytes, backing off to the
// nearest rune boundary so the truncated value stays valid utf-8 (required by
// the attributes-charset declaration), and warns on stderr when it truncates.
func ippSafeString(kind, name, s string) string {
	if len(s) <= maxIPPFieldLen {
		return s
	}
	n := maxIPPFieldLen
	for n > 0 && !utf8.ValidString(s[:n]) {
		n--
	}
	fmt.Fprintf(os.Stderr, "warning: %s %q is %d bytes, truncating to %d (IPP's length prefix is a uint16)\n", kind, name, len(s), n)
	return s[:n]
}

// IPP value tags (RFC 8010).
const (
	tagUnsupported     byte = 0x10
	tagInteger         byte = 0x21
	tagBoolean         byte = 0x22
	tagEnum            byte = 0x23
	tagOctetString     byte = 0x30
	tagTextWithoutLang byte = 0x41
	tagNameWithoutLang byte = 0x42
	tagKeyword         byte = 0x44
	tagURI             byte = 0x45
	tagCharset         byte = 0x47
	tagNaturalLanguage byte = 0x48
	tagMimeMediaType   byte = 0x49
	tagRangeOfInteger  byte = 0x33
	tagResolution      byte = 0x32
)

// IPP delimiter tags
const (
	tagOperationAttributes   byte = 0x01
	tagJobAttributes         byte = 0x02
	tagEndOfAttributes       byte = 0x03
	tagPrinterAttributes     byte = 0x04
	tagUnsupportedAttributes byte = 0x05 // names which requested attributes the printer rejected
)

// IPP operation ids
const (
	opPrintJob             uint16 = 0x0002
	opCancelJob            uint16 = 0x0008
	opGetJobAttributes     uint16 = 0x0009
	opGetJobs              uint16 = 0x000A
	opGetPrinterAttributes uint16 = 0x000B
)

type ippAttribute struct {
	Tag   byte
	Name  string
	Value string
	Group byte // the delimiter tag (tagJobAttributes etc.) this attribute was found under
}

type ippResponse struct {
	Version    uint16
	StatusCode uint16
	RequestID  uint32
	Attributes []ippAttribute
}

func writeAttribute(buf *bytes.Buffer, tag byte, name, value string) {
	origName := name
	name = ippSafeString("attribute name", name, name)
	value = ippSafeString("attribute value for", origName, value)
	buf.WriteByte(tag)
	binary.Write(buf, binary.BigEndian, uint16(len(name)))
	buf.WriteString(name)
	binary.Write(buf, binary.BigEndian, uint16(len(value)))
	buf.WriteString(value)
}

func writeIntegerAttribute(buf *bytes.Buffer, tag byte, name string, value int32) {
	name = ippSafeString("attribute name", name, name)
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
	name = ippSafeString("attribute name", name, name)
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
	name = ippSafeString("attribute name", name, name)
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
// given HTTP(S) endpoint and parses the response header and attributes.
// documentSize must be document's exact byte count (0 when document is nil)
// so Content-Length can be set explicitly instead of falling back to chunked
// transfer-encoding.
func sendIPP(endpoint string, request *bytes.Buffer, document io.Reader, documentSize int64) (*ippResponse, error) {
	var body io.Reader = request
	if document != nil {
		body = io.MultiReader(request, document)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("build http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/ipp")
	req.ContentLength = int64(request.Len()) + documentSize

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http post to %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		const maxIPPErrorBodyBytes = 4 << 10 // diagnostic text only, not a document
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxIPPErrorBodyBytes))
		return nil, fmt.Errorf("unexpected HTTP status %s from %s: %s", resp.Status, endpoint, string(data))
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxIPPResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read ipp response body: %w", err)
	}
	if len(raw) > maxIPPResponseBytes {
		return nil, fmt.Errorf("ipp response from %s exceeds the %d byte limit", endpoint, maxIPPResponseBytes)
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
	var group byte
	for pos < len(raw) {
		tag := raw[pos]
		pos++
		if tag == tagEndOfAttributes {
			break
		}
		if tag <= 0x0F {
			group = tag // track the open group so each attribute records which one it belongs to
			lastName = ""
			continue
		}
		if pos+2 > len(raw) {
			return nil, fmt.Errorf("truncated ipp response: name length prefix at offset %d exceeds %d-byte body", pos, len(raw))
		}
		nameLen := int(binary.BigEndian.Uint16(raw[pos : pos+2]))
		pos += 2
		if pos+nameLen > len(raw) {
			return nil, fmt.Errorf("truncated ipp response: %d-byte name at offset %d exceeds %d-byte body", nameLen, pos, len(raw))
		}
		name := string(raw[pos : pos+nameLen])
		pos += nameLen
		if name == "" {
			name = lastName // empty name = additional value of the previous multi-valued attribute (RFC 8010)
		} else {
			lastName = name
		}

		if pos+2 > len(raw) {
			return nil, fmt.Errorf("truncated ipp response: value length prefix at offset %d exceeds %d-byte body", pos, len(raw))
		}
		valueLen := int(binary.BigEndian.Uint16(raw[pos : pos+2]))
		pos += 2
		if pos+valueLen > len(raw) {
			return nil, fmt.Errorf("truncated ipp response: %d-byte value at offset %d exceeds %d-byte body", valueLen, pos, len(raw))
		}
		value := raw[pos : pos+valueLen]
		pos += valueLen

		r.Attributes = append(r.Attributes, ippAttribute{
			Tag:   tag,
			Name:  name,
			Value: decodeValue(tag, value),
			Group: group,
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

// statusName names an IPP status code. 0x0001/0x0002 are kept distinct from
// 0x0000: they mean the printer accepted the job but ignored, substituted, or
// found conflicting attributes, which is not a clean success.
func statusName(code uint16) string {
	switch code {
	case 0x0000:
		return "successful-ok"
	case 0x0001:
		return "successful-ok-ignored-or-substituted-attributes"
	case 0x0002:
		return "successful-ok-conflicting-attributes"
	}
	if code < 0x0100 {
		return fmt.Sprintf("successful-ok-0x%04x", code)
	}
	return fmt.Sprintf("error-0x%04x", code)
}
