package document

import (
	"sort"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/LehMichael/run-myscreens-lsp/internal/model"
	"github.com/LehMichael/run-myscreens-lsp/internal/protocol"
)

type Document struct {
	URI        string
	LanguageID string
	Version    int32
	Text       string
	Analysis   model.Analysis
	lineStarts []int
}

func New(uri, languageID string, version int32, text string) *Document {
	document := &Document{URI: uri, LanguageID: languageID, Version: version, Text: text}
	document.reindex()
	return document
}

func (d *Document) Replace(version int32, text string) {
	d.Version = version
	d.Text = text
	d.Analysis = model.Analysis{}
	d.reindex()
}

func (d *Document) PositionAt(byteOffset uint) protocol.Position {
	offset := int(byteOffset)
	if offset < 0 {
		offset = 0
	}
	if offset > len(d.Text) {
		offset = len(d.Text)
	}
	line := sort.Search(len(d.lineStarts), func(i int) bool { return d.lineStarts[i] > offset }) - 1
	if line < 0 {
		line = 0
	}
	lineStart := d.lineStarts[line]
	column := utf16Length([]byte(d.Text[lineStart:offset]))
	return protocol.Position{Line: uint32(line), Character: uint32(column)}
}

func (d *Document) Range(startByte, endByte uint) protocol.Range {
	if endByte < startByte {
		endByte = startByte
	}
	return protocol.Range{Start: d.PositionAt(startByte), End: d.PositionAt(endByte)}
}

func (d *Document) ByteOffsetAt(position protocol.Position) (uint, bool) {
	line := int(position.Line)
	if line < 0 || line >= len(d.lineStarts) {
		return 0, false
	}
	start := d.lineStarts[line]
	end := len(d.Text)
	if line+1 < len(d.lineStarts) {
		end = d.lineStarts[line+1] - 1
		if end > start && d.Text[end-1] == '\r' {
			end--
		}
	}
	targetUnits := int(position.Character)
	units := 0
	for offset := start; offset < end; {
		if units == targetUnits {
			return uint(offset), true
		}
		r, size := utf8.DecodeRuneInString(d.Text[offset:end])
		if size == 0 {
			break
		}
		runeUnits := 1
		if r != utf8.RuneError || size > 1 {
			runeUnits = len(utf16.Encode([]rune{r}))
		}
		if units+runeUnits > targetUnits {
			return 0, false
		}
		units += runeUnits
		offset += size
	}
	if units == targetUnits {
		return uint(end), true
	}
	return 0, false
}

func (d *Document) reindex() {
	d.lineStarts = []int{0}
	for index := 0; index < len(d.Text); index++ {
		if d.Text[index] == '\n' {
			d.lineStarts = append(d.lineStarts, index+1)
		}
	}
}

func utf16Length(text []byte) int {
	units := 0
	for len(text) > 0 {
		r, size := utf8.DecodeRune(text)
		if size == 0 {
			break
		}
		if r == utf8.RuneError && size == 1 {
			units++
		} else {
			units += len(utf16.Encode([]rune{r}))
		}
		text = text[size:]
	}
	return units
}
