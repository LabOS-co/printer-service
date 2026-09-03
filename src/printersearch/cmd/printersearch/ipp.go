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

// ippClientTimeout is httpClient's default Timeout, overridable per
// subcommand via -timeout (see setClientTimeout). Bounds the whole
// request/response round trip (dial through reading the body): without it,
// a target that accepts the TCP connection but never answers - a wedged
// cupsd, a dropped firewall rule - blocks the caller forever; for
// `bench -wait-completion` that meant a worker never returned and the
// whole run hung with no output at all, which is worse than the give-up
// -poll-timeout is supposed to produce.
const ippClientTimeout = 60 * time.Second

// httpClient is shared across every sendIPP call instead of allocating one
// per call (B4) - not for connection pooling (a nil Transport already
// defaults to http.DefaultTransport, so a fresh *http.Client per call
// reused the pool just as well), but so -timeout applies uniformly from one
// place, including to bench's many concurrent goroutines. Safe for
// concurrent use (http.Client's own contract); Timeout is set once via
// setClientTimeout before any goroutine starts (every call site parses
// flags and calls setClientTimeout before spawning workers), never mutated
// concurrently with a request in flight.
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

// maxIPPFieldLen is the largest name/value length IPP's own framing can
// encode in one attribute: RFC 8010's length prefix is a uint16. Without
// this guard, a name/value longer than this truncates only the LENGTH
// PREFIX (uint16(len(s)) wraps silently) while the FULL string is still
// written after it - desyncing every byte of the message that follows, not
// just this one field's content. The only externally-influenced input that
// can realistically reach this is a job name/title built from a long file
// path or an explicit -job-name flag; every other caller here passes a
// short, hardcoded attribute/keyword name.
const maxIPPFieldLen = 65535

// ippSafeString clamps s to at most maxIPPFieldLen bytes so the length
// prefix written to the wire and the bytes actually written always agree.
// kind/name identify the field in the warning printed to stderr only when
// truncation actually happens - built lazily, in the truncation branch, not
// on every call: this runs per attribute per request (bench.go calls it up
// to hundreds of times per run), and building a label string on every
// non-truncating call is a needless allocation in a benchmarking tool's own
// worker loop.
//
// The clamp backs off to the nearest rune boundary at or below
// maxIPPFieldLen rather than cutting at the raw byte index: IPP's own
// attributes-charset declaration (buildRequest) promises utf-8, and a value
// truncated mid-rune is invalid utf-8 in a message that says otherwise - a
// strict server can reject the whole request over that, not just this one
// field. Backing off can only ever shrink the cut, never grow it, so the
// uint16 length prefix stays correct either way.
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

// IPP value tags (RFC 8010). tagUnsupported (the out-of-band "unsupported"
// value, not the 0x05 unsupported-attributes group delimiter), tagOctetString,
// and tagTextWithoutLang are not referenced elsewhere yet - decodeValue's
// default case already renders them correctly as raw strings, and
// tagUnsupported is expected to be wired up when group structure is retained
// (plan item B2).
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
// given HTTP(S) endpoint and parses the response header + attributes.
// documentSize is the exact byte count document will yield (0 when document
// is nil): known up front by every caller (a *bytes.Buffer/[]byte length, or
// an *os.File's Stat().Size()), which is what lets Content-Length be set
// explicitly below instead of net/http falling back to chunked
// transfer-encoding for a body it can't measure itself.
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
		// A much smaller bound than the real response limit below: this is
		// diagnostic text for a stderr message, not a document, and 16 MiB
		// is unreasonably large for something %v-formatted straight into an
		// error string.
		const maxIPPErrorBodyBytes = 4 << 10
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
		// delimiter tags mark the start of an attribute group (operation/job/
		// printer/unsupported). Track which one is open so each attribute can
		// carry it - in particular so an attribute under
		// tagUnsupportedAttributes (the printer naming what it rejected) can be
		// told apart from a normal printer/job attribute once parsed.
		if tag <= 0x0F {
			group = tag
			lastName = "" // a new group starts a new attribute, per ippfix's reference parser
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
			name = lastName // additional value for a multi-valued attribute
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

// statusName names an IPP status code. 0x0001/0x0002 are distinguished from
// 0x0000 rather than collapsed into "successful-ok": the printer accepted the
// job but ignored, substituted, or found conflicting job attributes - exactly
// the failure mode (a silently dropped media/printer-resolution pin) this
// project spent weeks diagnosing.
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
