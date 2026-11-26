// xmlsong.go writes a JSON-shaped object as a Subsonic XML element.
//
// Works with: render.go, which calls encodeObject for every song and error.
//
// Subsonic XML puts scalar fields in attributes. OpenSubsonic adds structured
// fields, which its XML form writes as child elements: an object becomes one
// child (replayGain), an array of objects becomes one child per item (genres,
// artists, contributors), and an array of strings becomes one child per value
// with the value as text (moods).
package subsonic

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strconv"
)

func encodeObject(enc *xml.Encoder, name string, object map[string]any) error {
	start := xml.StartElement{Name: xml.Name{Local: name}}
	var children []string
	for _, key := range sortedKeys(object) {
		value := object[key]
		if isScalar(value) {
			start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: key}, Value: scalarText(value)})
			continue
		}
		children = append(children, key)
	}
	if err := enc.EncodeToken(start); err != nil {
		return err
	}
	for _, key := range children {
		if err := encodeChild(enc, key, object[key]); err != nil {
			return err
		}
	}
	return enc.EncodeToken(start.End())
}

func encodeChild(enc *xml.Encoder, name string, value any) error {
	switch v := value.(type) {
	case map[string]any:
		return encodeObject(enc, name, v)
	case []any:
		for _, item := range v {
			switch it := item.(type) {
			case map[string]any:
				if err := encodeObject(enc, name, it); err != nil {
					return err
				}
			default:
				if !isScalar(it) {
					continue
				}
				start := xml.StartElement{Name: xml.Name{Local: name}}
				if err := enc.EncodeElement(scalarText(it), start); err != nil {
					return err
				}
			}
		}
	}
	return nil // null and unknown shapes are dropped, as upstream XML omits them
}

func isScalar(value any) bool {
	switch value.(type) {
	case string, bool, json.Number, float64, int, int64:
		return true
	}
	return false
}

func scalarText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return fmt.Sprint(value)
}
