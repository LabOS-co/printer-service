// ippfix is an IPP reverse proxy that sits between CUPS and a real printer
// and sanitizes the printer's Get-Printer-Attributes response using a
// template of known-good default values.
//
// The original POC proxy (C:\printerSearch\ippfix) hardcoded a single fix:
// replace empty naturalLanguage-tagged attribute values with "en-us". That
// worked, but it only defends against the one bug class we happened to find
// on this printer, and stricter IPP validators (cups-filters 2.0.0) might
// reject the response for other reasons we haven't hit yet.
//
// This version generalizes the idea: a template captures a full "known
// good" snapshot of the printer's printer-attributes group (built once from
// a real capture, with known-broken fields corrected). At request time,
// every attribute in the printer-attributes group is checked against the
// template:
//   - if the printer's live value for that attribute is present and
//     non-empty, it is kept as-is (the printer's real answer always wins);
//   - if the live value is empty, or the attribute is missing entirely, the
//     template's value is substituted instead.
//
// This means we always hand CUPS a complete, well-formed attribute set,
// regardless of which specific fields this printer's firmware happens to
// get wrong.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
)

// IPP tags relevant here (RFC 8010).
const (
	tagEndOfAttributes  byte = 0x03
	tagPrinterAttrGroup byte = 0x04
	tagOperationAttrs   byte = 0x01
)

// entry is one element of the flat, ordered token stream that makes up an
// IPP message body after the 8-byte header: either a group delimiter
// (tag <= 0x0F, name/value unused) or an attribute value.
type entry struct {
	tag         byte
	nameOnWire  string // as encoded: empty for a continuation value of a multi-valued attribute
	logicalName string // nameOnWire, or the owning attribute's name for continuation values
	value       []byte
}

func parseEntries(body []byte) []entry {
	var entries []entry
	pos := 0
	last := ""
	for pos < len(body) {
		tag := body[pos]
		pos++
		if tag <= 0x0f {
			entries = append(entries, entry{tag: tag})
			if tag == tagEndOfAttributes {
				break
			}
			last = ""
			continue
		}
		if pos+2 > len(body) {
			break
		}
		nameLen := int(binary.BigEndian.Uint16(body[pos : pos+2]))
		pos += 2
		if pos+nameLen > len(body) {
			break
		}
		name := string(body[pos : pos+nameLen])
		pos += nameLen

		logical := name
		if name == "" {
			logical = last
		} else {
			last = name
		}

		if pos+2 > len(body) {
			break
		}
		valLen := int(binary.BigEndian.Uint16(body[pos : pos+2]))
		pos += 2
		if pos+valLen > len(body) {
			break
		}
		value := body[pos : pos+valLen]
		pos += valLen

		entries = append(entries, entry{tag: tag, nameOnWire: name, logicalName: logical, value: value})
	}
	return entries
}

func encodeEntries(header []byte, entries []entry) []byte {
	out := make([]byte, 0, len(header)+len(entries)*16)
	out = append(out, header...)
	for _, e := range entries {
		out = append(out, e.tag)
		if e.tag <= 0x0f {
			continue
		}
		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(e.nameOnWire)))
		out = append(out, lenBuf[:]...)
		out = append(out, e.nameOnWire...)
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(e.value)))
		out = append(out, lenBuf[:]...)
		out = append(out, e.value...)
	}
	return out
}

// ---- template ----

type templateAttr struct {
	Tag    byte     `json:"tag"`
	Name   string   `json:"name"`
	Values []string `json:"values"` // base64-encoded raw value bytes, one per value
}

type template struct {
	Attrs []templateAttr `json:"attrs"` // ordered, printer-attributes group only
}

func (t *template) index() map[string]templateAttr {
	m := make(map[string]templateAttr, len(t.Attrs))
	for _, a := range t.Attrs {
		m[a.Name] = a
	}
	return m
}

func templateAttrToEntries(a templateAttr) []entry {
	entries := make([]entry, 0, len(a.Values))
	for i, v := range a.Values {
		raw, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			raw = nil
		}
		nameOnWire := a.Name
		if i > 0 {
			nameOnWire = ""
		}
		entries = append(entries, entry{tag: a.Tag, nameOnWire: nameOnWire, logicalName: a.Name, value: raw})
	}
	return entries
}

// tagAllowsEmptyValue reports whether a zero-length value is normal (not a
// sign of a broken attribute) for this tag: the out-of-band value tags
// (unsupported/unknown/no-value/etc., 0x10-0x1F) and the begCollection
// (0x34) / endCollection (0x37) structural markers are always zero-length
// by design, not evidence of a malformed response.
func tagAllowsEmptyValue(tag byte) bool {
	if tag >= 0x10 && tag <= 0x1f {
		return true
	}
	return tag == 0x34 || tag == 0x37
}

// tagsRequiringNonEmpty are IPP value-tag types where RFC 8011 requires a
// non-empty value; this printer's firmware violates that for at least one
// naturalLanguage-tagged attribute. Treated generically by tag, not by a
// specific attribute name, so any other attribute of the same tag type that
// turns out broken is covered too.
var tagsRequiringNonEmpty = map[byte]string{
	0x48: "en-us", // naturalLanguage
	0x47: "utf-8", // charset
}

// buildTemplateFromCapture extracts the printer-attributes group from a raw
// Get-Printer-Attributes response and applies the known tag-level fixups,
// producing the "golden" template.
func buildTemplateFromCapture(body []byte) template {
	if len(body) < 8 {
		return template{}
	}
	entries := parseEntries(body[8:])

	inPrinterGroup := false
	var tmpl template
	byName := map[string]int{} // name -> index in tmpl.Attrs

	for _, e := range entries {
		if e.tag <= 0x0f {
			inPrinterGroup = e.tag == tagPrinterAttrGroup
			continue
		}
		if !inPrinterGroup {
			continue
		}
		value := e.value
		if def, ok := tagsRequiringNonEmpty[e.tag]; ok && len(value) == 0 {
			value = []byte(def)
		}
		if e.nameOnWire != "" {
			tmpl.Attrs = append(tmpl.Attrs, templateAttr{Tag: e.tag, Name: e.logicalName, Values: []string{base64.StdEncoding.EncodeToString(value)}})
			byName[e.logicalName] = len(tmpl.Attrs) - 1
		} else if idx, ok := byName[e.logicalName]; ok {
			tmpl.Attrs[idx].Values = append(tmpl.Attrs[idx].Values, base64.StdEncoding.EncodeToString(value))
		}
	}
	return tmpl
}

// fixEmptyRequiredTags applies the generic tag-level fix (empty
// naturalLanguage/charset values get a sane default) to every attribute in
// the message, regardless of which group it's in. This matters because the
// empty attributes-natural-language value shows up in the RESPONSE's own
// operation-attributes group (echoed back per RFC 8011), not just inside
// the printer-attributes group that the by-name template overlay targets —
// a strict client can reject the message over that alone, before it ever
// gets to inspecting printer capabilities.
func fixEmptyRequiredTags(entries []entry) {
	for i := range entries {
		e := &entries[i]
		if e.tag <= 0x0f {
			continue
		}
		if def, ok := tagsRequiringNonEmpty[e.tag]; ok && len(e.value) == 0 {
			e.value = []byte(def)
		}
	}
}

// applyTemplate fixes empty naturalLanguage/charset values everywhere in the
// message, then additionally walks the printer-attributes group and, for
// each attribute there, keeps the live value if present/non-empty, or
// substitutes the template's value otherwise. Attributes present in the
// template but entirely absent from the live response are appended. The
// by-name overlay only runs if a template was loaded; the tag-level fix
// above always runs.
func applyTemplate(body []byte, tmpl template) []byte {
	if len(body) < 8 {
		return body
	}
	header := body[:8]
	entries := parseEntries(body[8:])
	fixEmptyRequiredTags(entries)

	if len(tmpl.Attrs) == 0 {
		return encodeEntries(header, entries)
	}
	byName := tmpl.index()

	var out []entry
	seen := map[string]bool{}
	inPrinterGroup := false
	groupOpen := false

	flushMissing := func() {
		if !groupOpen {
			return
		}
		for _, a := range tmpl.Attrs {
			if seen[a.Name] {
				continue
			}
			out = append(out, templateAttrToEntries(a)...)
		}
		groupOpen = false
	}

	i := 0
	for i < len(entries) {
		e := entries[i]
		if e.tag <= 0x0f {
			if inPrinterGroup {
				flushMissing()
			}
			inPrinterGroup = e.tag == tagPrinterAttrGroup
			if inPrinterGroup {
				groupOpen = true
				seen = map[string]bool{}
			}
			out = append(out, e)
			i++
			continue
		}
		if !inPrinterGroup {
			out = append(out, e)
			i++
			continue
		}

		// collect the full run of entries for this attribute (name + any
		// zero-length-name continuation values)
		name := e.logicalName
		j := i + 1
		for j < len(entries) && entries[j].tag > 0x0f && entries[j].nameOnWire == "" && entries[j].logicalName == name {
			j++
		}
		run := entries[i:j]
		seen[name] = true

		valid := true
		for _, r := range run {
			if len(r.value) == 0 && !tagAllowsEmptyValue(r.tag) {
				valid = false
				break
			}
		}

		if valid {
			out = append(out, run...)
		} else if def, ok := byName[name]; ok {
			out = append(out, templateAttrToEntries(def)...)
		} else {
			out = append(out, run...) // nothing better to substitute
		}
		i = j
	}
	flushMissing()

	return encodeEntries(header, out)
}

func main() {
	listen := flag.String("listen", ":6310", "address for this proxy to listen on")
	target := flag.String("target", "http://192.168.252.210:631", "real printer's base IPP URL")
	templatePath := flag.String("template", "", "path to a template JSON file (required for the overlay fix; see -gen-template)")
	genTemplate := flag.String("gen-template", "", "capture the printer's real response once, apply known fixups, write a template to this path, then exit")
	flag.Parse()

	if *genTemplate != "" {
		if err := runGenTemplate(*target, *genTemplate); err != nil {
			log.Fatalf("gen-template failed: %v", err)
		}
		log.Printf("template written to %s", *genTemplate)
		return
	}

	var tmpl template
	if *templatePath != "" {
		data, err := os.ReadFile(*templatePath)
		if err != nil {
			log.Fatalf("reading template %s: %v", *templatePath, err)
		}
		if err := json.Unmarshal(data, &tmpl); err != nil {
			log.Fatalf("parsing template %s: %v", *templatePath, err)
		}
		log.Printf("loaded template with %d attributes from %s", len(tmpl.Attrs), *templatePath)
	} else {
		log.Printf("no -template given; proxying without any attribute overlay (pure pass-through)")
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		reqBody, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		req, err := http.NewRequest(r.Method, *target+r.URL.Path, bytes.NewReader(reqBody))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("Content-Type", r.Header.Get("Content-Type"))

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		if resp.Header.Get("Content-Type") == "application/ipp" && len(tmpl.Attrs) > 0 {
			respBody = applyTemplate(respBody, tmpl)
		}

		w.Header().Set("Content-Type", "application/ipp")
		w.Header().Set("Content-Length", strconv.Itoa(len(respBody)))
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
	})

	log.Printf("ippfix listening on %s, forwarding to %s (template-overlay: %v)", *listen, *target, *templatePath != "")
	log.Fatal(http.ListenAndServe(*listen, nil))
}

// runGenTemplate sends a Get-Printer-Attributes request directly to the real
// printer, builds a template from the response, and writes it as JSON.
func runGenTemplate(target, outPath string) error {
	uri := target + "/ipp/print"
	buf := &bytes.Buffer{}
	binary.Write(buf, binary.BigEndian, uint16(0x0200)) // IPP 2.0
	binary.Write(buf, binary.BigEndian, uint16(0x000B)) // Get-Printer-Attributes
	binary.Write(buf, binary.BigEndian, uint32(1))      // request-id
	buf.WriteByte(tagOperationAttrs)
	writeAttr(buf, 0x47, "attributes-charset", "utf-8")
	writeAttr(buf, 0x48, "attributes-natural-language", "en-us")
	writeAttr(buf, 0x45, "printer-uri", uri)
	buf.WriteByte(tagEndOfAttributes)

	httpReq, err := http.NewRequest(http.MethodPost, uri, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/ipp")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	tmpl := buildTemplateFromCapture(raw)
	data, err := json.MarshalIndent(tmpl, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(outPath, data, 0644)
}

func writeAttr(buf *bytes.Buffer, tag byte, name, value string) {
	buf.WriteByte(tag)
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(name)))
	buf.Write(lenBuf[:])
	buf.WriteString(name)
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(value)))
	buf.Write(lenBuf[:])
	buf.WriteString(value)
}
