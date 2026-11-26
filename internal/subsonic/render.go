// render.go writes Subsonic response envelopes in the client's format.
//
// Works with: xmlsong.go (song objects as XML elements), request.go (Format).
// Called by internal/server for cache hits, misses and auth failures.
//
// Song objects are kept exactly as the upstream sent them in JSON (see
// specs/subslip.md section 10), so a JSON reply is the upstream's own song
// data and an XML reply is the same data as attributes and child elements.
package subsonic

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"sort"
)

// Song is one Subsonic song ("Child") object as upstream JSON.
type Song = map[string]any

// Meta holds the envelope's scalar fields (version, type, serverVersion,
// openSubsonic). They are copied from the upstream's own responses so a reply
// from the cache looks like it came from the upstream.
type Meta map[string]any

// DefaultMeta is used when no upstream envelope has been seen.
func DefaultMeta() Meta { return Meta{"version": "1.16.1"} }

// MetaFrom copies the scalar fields of an upstream "subsonic-response" object,
// leaving out status and error.
func MetaFrom(envelope map[string]any) Meta {
	meta := Meta{}
	for key, value := range envelope {
		if key == "status" || key == "error" {
			continue
		}
		switch value.(type) {
		case string, bool, json.Number, float64:
			meta[key] = value
		}
	}
	if _, ok := meta["version"]; !ok {
		meta["version"] = "1.16.1"
	}
	return meta
}

// ElementFor names the result element for a similar-songs method.
func ElementFor(method string) string {
	if method == "getsimilarsongs" {
		return "similarSongs"
	}
	return "similarSongs2"
}

// WriteSongs writes an ok envelope holding songs under element.
func WriteSongs(w http.ResponseWriter, format string, meta Meta, element string, songs []Song) error {
	if format == "json" {
		body := envelopeJSON(meta, "ok")
		if songs == nil {
			songs = []Song{}
		}
		body[element] = map[string]any{"song": songs}
		return writeJSON(w, body)
	}
	return writeXML(w, meta, "ok", func(enc *xml.Encoder) error {
		start := xml.StartElement{Name: xml.Name{Local: element}}
		if err := enc.EncodeToken(start); err != nil {
			return err
		}
		for _, song := range songs {
			if err := encodeObject(enc, "song", song); err != nil {
				return err
			}
		}
		return enc.EncodeToken(start.End())
	})
}

// WriteError writes a failed envelope with a Subsonic error code and message.
func WriteError(w http.ResponseWriter, format string, meta Meta, code int, message string) error {
	if format == "json" {
		body := envelopeJSON(meta, "failed")
		body["error"] = map[string]any{"code": code, "message": message}
		return writeJSON(w, body)
	}
	return writeXML(w, meta, "failed", func(enc *xml.Encoder) error {
		return encodeObject(enc, "error", map[string]any{"code": json.Number(itoa(code)), "message": message})
	})
}

func envelopeJSON(meta Meta, status string) map[string]any {
	body := map[string]any{}
	for key, value := range meta {
		body[key] = value
	}
	body["status"] = status
	return body
}

func writeJSON(w http.ResponseWriter, body map[string]any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(map[string]any{"subsonic-response": body})
}

func writeXML(w http.ResponseWriter, meta Meta, status string, inner func(*xml.Encoder) error) error {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	attrs := []xml.Attr{
		{Name: xml.Name{Local: "xmlns"}, Value: "http://subsonic.org/restapi"},
		{Name: xml.Name{Local: "status"}, Value: status},
	}
	for _, key := range sortedKeys(meta) {
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: key}, Value: scalarText(meta[key])})
	}
	start := xml.StartElement{Name: xml.Name{Local: "subsonic-response"}, Attr: attrs}
	if err := enc.EncodeToken(start); err != nil {
		return err
	}
	if err := inner(enc); err != nil {
		return err
	}
	if err := enc.EncodeToken(start.End()); err != nil {
		return err
	}
	return enc.Flush()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
