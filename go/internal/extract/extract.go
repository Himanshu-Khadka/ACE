// Package extract pulls plain text out of html, pdf, pptx and docx so it can
// be searched.
package extract

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ledongthuc/pdf"
	"golang.org/x/net/html"
)

var blockTags = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "table": true, "ul": true, "ol": true,
	"section": true, "blockquote": true, "pre": true,
}

// HTML turns canvas html (pages, descriptions, announcements) into text.
func HTML(s string) string {
	if s == "" {
		return ""
	}
	z := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	skip := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			return tidy(b.String())
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if tag == "script" || tag == "style" {
				skip++
			}
			if blockTags[tag] {
				b.WriteString("\n")
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if (tag == "script" || tag == "style") && skip > 0 {
				skip--
			}
			if blockTags[tag] {
				b.WriteString("\n")
			}
		case html.TextToken:
			if skip == 0 {
				b.Write(z.Text())
			}
		}
	}
}

var spaces = regexp.MustCompile(`[ \t\r\f\v\x{a0}]+`)

// tidy collapses spaces and drops empty lines.
func tidy(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(spaces.ReplaceAllString(line, " "))
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

var plainText = map[string]bool{
	".txt": true, ".md": true, ".py": true, ".c": true, ".h": true, ".cpp": true, ".java": true,
	".go": true, ".js": true, ".ts": true, ".csv": true, ".json": true, ".sql": true, ".r": true,
}

// File returns the text of a downloaded file, or "" for types we can't read
// (images, zips, videos...). One broken file shouldn't stop the whole sync, so
// errors end up in the text instead.
func File(path string) (text string) {
	defer func() {
		// the pdf library panics on some weird pdfs
		if r := recover(); r != nil {
			text = fmt.Sprintf("[could not extract text: %v]", r)
		}
	}()
	ext := strings.ToLower(filepath.Ext(path))
	var err error
	switch {
	case ext == ".pdf":
		text, err = pdfText(path)
	case ext == ".pptx":
		text, err = officeText(path, "ppt/slides/slide", "slide")
	case ext == ".docx":
		text, err = officeText(path, "word/document", "")
	case ext == ".html" || ext == ".htm":
		var raw []byte
		raw, err = os.ReadFile(path)
		text = HTML(string(raw))
	case plainText[ext]:
		var raw []byte
		raw, err = os.ReadFile(path)
		text = strings.ToValidUTF8(string(raw), "")
	}
	if err != nil {
		return fmt.Sprintf("[could not extract text: %v]", err)
	}
	return text
}

func pdfText(path string) (string, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var b strings.Builder
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		t, err := p.GetPlainText(nil)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "[page %d]\n%s\n\n", i, strings.TrimSpace(t))
	}
	return strings.TrimSpace(b.String()), nil
}

var trailingNum = regexp.MustCompile(`(\d+)\.xml$`)

// officeText reads pptx/docx, which are just zip files full of xml. Text is in
// <a:t> (slides) or <w:t> (word) elements and paragraphs are <a:p>/<w:p>.
func officeText(path, prefix, label string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer zr.Close()

	var parts []*zip.File
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, prefix) && strings.HasSuffix(f.Name, ".xml") && !strings.Contains(f.Name, "_rels") {
			parts = append(parts, f)
		}
	}
	// slide10 should come after slide9
	num := func(name string) int {
		if m := trailingNum.FindStringSubmatch(name); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
		return 0
	}
	sort.Slice(parts, func(i, j int) bool { return num(parts[i].Name) < num(parts[j].Name) })

	var out []string
	for i, f := range parts {
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		t, err := xmlText(rc)
		rc.Close()
		if err != nil {
			return "", err
		}
		if label != "" {
			t = fmt.Sprintf("[%s %d]\n%s", label, i+1, t)
		}
		out = append(out, t)
	}
	return strings.Join(out, "\n\n"), nil
}

func xmlText(r io.Reader) (string, error) {
	d := xml.NewDecoder(r)
	var b strings.Builder
	inText := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return tidy(b.String()), nil
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				b.WriteString("\t")
			case "br":
				b.WriteString("\n")
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				b.WriteString("\n")
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
	}
}
