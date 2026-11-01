package host

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MaxDrawingBytes caps what one drawing may weigh, before and after
// cleaning. A drawing is a sketch: a model that writes more than this is
// illustrating, at a cost of minutes per picture.
const MaxDrawingBytes = 32 * 1024

var (
	ErrNotSVG        = errors.New("not an svg document")
	ErrDrawingTooBig = fmt.Errorf("drawing exceeds %d bytes", MaxDrawingBytes)
)

// svgElements is everything a drawing may be made of: shapes, text, paint
// and structure. Anything that embeds another document (foreignObject,
// image, script, iframe), runs code or loads from elsewhere is left out.
var svgElements = map[string]bool{
	"svg": true, "g": true, "defs": true, "symbol": true, "use": true, "title": true, "desc": true,
	"path": true, "rect": true, "circle": true, "ellipse": true, "line": true, "polyline": true, "polygon": true,
	"text": true, "tspan": true, "textPath": true,
	"linearGradient": true, "radialGradient": true, "stop": true, "pattern": true,
	"clipPath": true, "mask": true, "marker": true,
	"filter": true, "feGaussianBlur": true, "feOffset": true, "feBlend": true, "feMerge": true, "feMergeNode": true,
	"feColorMatrix": true, "feFlood": true, "feComposite": true, "feDropShadow": true, "feTurbulence": true, "feDisplacementMap": true,
}

// svgAttributes are the presentation and geometry attributes a drawing
// needs. Event handlers (on*), external references and style sheets with
// url() are refused in cleanAttribute rather than listed here.
var svgAttributes = map[string]bool{
	"id": true, "class": true, "xmlns": true, "xmlns:xlink": true, "version": true,
	"viewBox": true, "width": true, "height": true, "preserveAspectRatio": true,
	"x": true, "y": true, "x1": true, "y1": true, "x2": true, "y2": true, "dx": true, "dy": true,
	"cx": true, "cy": true, "r": true, "rx": true, "ry": true, "fx": true, "fy": true,
	"d": true, "points": true, "transform": true, "pathLength": true,
	"fill": true, "fill-opacity": true, "fill-rule": true, "opacity": true,
	"stroke": true, "stroke-width": true, "stroke-linecap": true, "stroke-linejoin": true,
	"stroke-dasharray": true, "stroke-dashoffset": true, "stroke-opacity": true, "stroke-miterlimit": true,
	"font-family": true, "font-size": true, "font-weight": true, "font-style": true, "text-anchor": true,
	"dominant-baseline": true, "alignment-baseline": true, "letter-spacing": true, "textLength": true, "lengthAdjust": true,
	"offset": true, "stop-color": true, "stop-opacity": true, "gradientUnits": true, "gradientTransform": true, "spreadMethod": true,
	"patternUnits": true, "patternContentUnits": true, "patternTransform": true,
	"clip-path": true, "clip-rule": true, "mask": true, "marker-start": true, "marker-mid": true, "marker-end": true,
	"markerWidth": true, "markerHeight": true, "refX": true, "refY": true, "orient": true, "markerUnits": true,
	"filter": true, "stdDeviation": true, "in": true, "in2": true, "result": true, "mode": true, "operator": true,
	"values": true, "type": true, "flood-color": true, "flood-opacity": true, "baseFrequency": true, "numOctaves": true, "seed": true, "scale": true,
	"style": true, "href": true, "xlink:href": true, "startOffset": true, "visibility": true, "display": true,
	"shape-rendering": true, "paint-order": true, "vector-effect": true, "overflow": true, "xml:space": true,
}

// CleanSVG keeps of a drawing only what draws. It parses the document, drops
// every element and attribute outside the allow-lists, refuses references
// that leave the document, and writes the result back out. The returned
// bytes are what is stored and shown, never the model's own text.
func CleanSVG(src []byte) ([]byte, error) {
	if len(src) > MaxDrawingBytes {
		return nil, ErrDrawingTooBig
	}
	dec := xml.NewDecoder(bytes.NewReader(src))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity

	var out bytes.Buffer
	enc := xml.NewEncoder(&out)
	// One entry per open element: whether it is written out, unwrapped (its
	// children kept, itself dropped) or skipped with everything inside it.
	var stack []elementMode
	seenRoot := false
	for {
		tok, err := dec.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNotSVG, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			if !seenRoot {
				if name != "svg" {
					return nil, ErrNotSVG
				}
				seenRoot = true
			}
			mode := modeFor(name, stack)
			stack = append(stack, mode)
			if mode != modeEmit {
				continue
			}
			start := xml.StartElement{Name: xml.Name{Local: name}}
			for _, a := range t.Attr {
				if attr, ok := cleanAttribute(name, a); ok {
					start.Attr = append(start.Attr, attr)
				}
			}
			if name == "svg" {
				start.Attr = ensureNamespace(start.Attr)
			}
			if err := enc.EncodeToken(start); err != nil {
				return nil, err
			}
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, ErrNotSVG
			}
			mode := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if mode != modeEmit {
				continue
			}
			if err := enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: t.Name.Local}}); err != nil {
				return nil, err
			}
		case xml.CharData:
			if len(stack) == 0 || skipped(stack) {
				continue
			}
			if err := enc.EncodeToken(xml.CharData(t)); err != nil {
				return nil, err
			}
		}
	}
	if !seenRoot {
		return nil, ErrNotSVG
	}
	if err := enc.Flush(); err != nil {
		return nil, err
	}
	if out.Len() > MaxDrawingBytes {
		return nil, ErrDrawingTooBig
	}
	return out.Bytes(), nil
}

type elementMode int

const (
	modeEmit elementMode = iota
	modeUnwrap
	modeSkip
)

// unwrapped elements are dropped but their content stays: a link around a
// label loses the link and keeps the label.
var unwrapped = map[string]bool{"a": true}

func modeFor(name string, stack []elementMode) elementMode {
	if skipped(stack) {
		return modeSkip
	}
	switch {
	case svgElements[name]:
		return modeEmit
	case unwrapped[name]:
		return modeUnwrap
	default:
		return modeSkip
	}
}

func skipped(stack []elementMode) bool {
	return len(stack) > 0 && stack[len(stack)-1] == modeSkip
}

func attrName(a xml.Attr) string {
	if a.Name.Space == "" {
		return a.Name.Local
	}
	if a.Name.Space == "xmlns" {
		return "xmlns:" + a.Name.Local
	}
	if a.Name.Space == "xlink" || strings.HasSuffix(a.Name.Space, "/xlink") {
		return "xlink:" + a.Name.Local
	}
	if a.Name.Space == "xml" || strings.HasSuffix(a.Name.Space, "/XML/1998/namespace") {
		return "xml:" + a.Name.Local
	}
	return a.Name.Space + ":" + a.Name.Local
}

// cleanAttribute decides one attribute. A reference (href, url()) may only
// point inside the document, so a drawing can use its own gradients and
// markers but never fetch, embed or navigate.
func cleanAttribute(element string, a xml.Attr) (xml.Attr, bool) {
	name := attrName(a)
	value := strings.TrimSpace(a.Value)
	if strings.HasPrefix(strings.ToLower(name), "on") {
		return xml.Attr{}, false
	}
	if !svgAttributes[name] {
		return xml.Attr{}, false
	}
	switch name {
	case "href", "xlink:href":
		if element == "use" || element == "textPath" {
			if !strings.HasPrefix(value, "#") {
				return xml.Attr{}, false
			}
		} else {
			return xml.Attr{}, false
		}
	case "style":
		lower := strings.ToLower(value)
		if strings.Contains(lower, "url(") || strings.Contains(lower, "expression") || strings.Contains(lower, "@import") || strings.Contains(lower, "behavior") {
			return xml.Attr{}, false
		}
	case "xmlns":
		if value != "http://www.w3.org/2000/svg" {
			return xml.Attr{}, false
		}
	case "xmlns:xlink":
		if value != "http://www.w3.org/1999/xlink" {
			return xml.Attr{}, false
		}
	default:
		lower := strings.ToLower(value)
		if strings.Contains(lower, "url(") && !strings.Contains(lower, "url(#") {
			return xml.Attr{}, false
		}
		if strings.Contains(lower, "javascript:") || strings.Contains(lower, "data:") {
			return xml.Attr{}, false
		}
	}
	return xml.Attr{Name: xml.Name{Local: name}, Value: value}, true
}

func ensureNamespace(attrs []xml.Attr) []xml.Attr {
	for _, a := range attrs {
		if a.Name.Local == "xmlns" {
			return attrs
		}
	}
	return append([]xml.Attr{{Name: xml.Name{Local: "xmlns"}, Value: "http://www.w3.org/2000/svg"}}, attrs...)
}
