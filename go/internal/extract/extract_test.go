package extract

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTML(t *testing.T) {
	got := HTML(`<h2>Lab 3</h2><p>Due   <b>Friday</b></p><script>alert(1)</script><ul><li>part a</li><li>part&nbsp;b</li></ul>`)
	want := "Lab 3\nDue Friday\npart a\npart b"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	f.Close()
}

func TestPPTXSlideOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lec.pptx")
	slide := func(s string) string {
		return `<p:sld xmlns:p="p" xmlns:a="a"><a:p><a:r><a:t>` + s + `</a:t></a:r></a:p></p:sld>`
	}
	writeZip(t, path, map[string]string{
		"ppt/slides/slide10.xml":           slide("ten"),
		"ppt/slides/slide2.xml":            slide("two"),
		"ppt/slides/slide1.xml":            slide("one"),
		"ppt/slides/_rels/slide1.xml.rels": "<x/>",
	})
	got := File(path)
	if !strings.Contains(got, "[slide 1]\none") || strings.Index(got, "two") > strings.Index(got, "ten") {
		t.Fatalf("bad slide text/order: %q", got)
	}
}

func TestDOCX(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.docx")
	writeZip(t, path, map[string]string{
		"word/document.xml": `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>Hello</w:t></w:r><w:r><w:t xml:space="preserve"> world</w:t></w:r></w:p><w:p><w:r><w:t>second</w:t></w:r></w:p></w:body></w:document>`,
	})
	if got := File(path); got != "Hello world\nsecond" {
		t.Fatalf("got %q", got)
	}
}

func TestUnknownTypeIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pic.png")
	os.WriteFile(path, []byte{0x89, 'P', 'N', 'G'}, 0o644)
	if got := File(path); got != "" {
		t.Fatalf("got %q", got)
	}
}
