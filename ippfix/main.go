// ippfix is a minimal IPP reverse proxy that patches one specific firmware
// bug in this printer's IPP responses: naturalLanguage-tagged attributes
// (attributes-natural-language, natural-language-configured,
// generated-natural-language-supported) come back with a zero-length value,
// which violates RFC 8011 section 5.1.9 and makes strict IPP clients (CUPS's
// driverless PPD generator included) refuse to use the capability response.
// Everything else is passed through byte-for-byte unchanged.
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"io"
	"log"
	"net/http"
	"strconv"
)

const tagNaturalLanguage = 0x48

func fixIPPBody(body []byte) []byte {
	if len(body) < 8 {
		return body
	}
	out := make([]byte, 0, len(body)+64)
	out = append(out, body[:8]...)
	pos := 8
	for pos < len(body) {
		tag := body[pos]
		if tag <= 0x0f {
			out = append(out, tag)
			pos++
			if tag == 0x03 { // end-of-attributes-tag
				out = append(out, body[pos:]...)
				return out
			}
			continue
		}
		if pos+3 > len(body) {
			out = append(out, body[pos:]...)
			break
		}
		nameLen := int(binary.BigEndian.Uint16(body[pos+1 : pos+3]))
		nameStart := pos + 3
		nameEnd := nameStart + nameLen
		if nameEnd+2 > len(body) {
			out = append(out, body[pos:]...)
			break
		}
		valueLen := int(binary.BigEndian.Uint16(body[nameEnd : nameEnd+2]))
		valueStart := nameEnd + 2
		valueEnd := valueStart + valueLen
		if valueEnd > len(body) {
			out = append(out, body[pos:]...)
			break
		}
		value := body[valueStart:valueEnd]
		if tag == tagNaturalLanguage && len(value) == 0 {
			value = []byte("en-us")
		}

		out = append(out, tag)
		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], uint16(nameLen))
		out = append(out, lenBuf[:]...)
		out = append(out, body[nameStart:nameEnd]...)
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(value)))
		out = append(out, lenBuf[:]...)
		out = append(out, value...)

		pos = valueEnd
	}
	return out
}

func main() {
	listen := flag.String("listen", ":6310", "address for this proxy to listen on")
	target := flag.String("target", "http://192.168.252.210:631", "real printer's base IPP URL")
	flag.Parse()

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

		if resp.Header.Get("Content-Type") == "application/ipp" {
			respBody = fixIPPBody(respBody)
		}

		w.Header().Set("Content-Type", "application/ipp")
		w.Header().Set("Content-Length", strconv.Itoa(len(respBody)))
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
	})

	log.Printf("ippfix listening on %s, forwarding to %s (patching empty naturalLanguage attributes)", *listen, *target)
	log.Fatal(http.ListenAndServe(*listen, nil))
}
