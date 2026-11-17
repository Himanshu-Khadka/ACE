"""Get plain text out of files/HTML so it can be searched."""
from __future__ import annotations

from pathlib import Path

from bs4 import BeautifulSoup


def html_to_text(html: str | None) -> str:
    if not html:
        return ""
    return BeautifulSoup(html, "html.parser").get_text("\n", strip=True)


def file_to_text(path: Path) -> str:
    suffix = path.suffix.lower()
    try:
        if suffix == ".pdf":
            from pypdf import PdfReader

            reader = PdfReader(str(path))
            return "\n\n".join(
                f"[page {i}]\n{p.extract_text() or ''}" for i, p in enumerate(reader.pages, 1)
            )
        if suffix == ".pptx":
            from pptx import Presentation

            slides = []
            for i, slide in enumerate(Presentation(str(path)).slides, 1):
                texts = [s.text_frame.text for s in slide.shapes if s.has_text_frame]
                slides.append(f"[slide {i}]\n" + "\n".join(texts))
            return "\n\n".join(slides)
        if suffix == ".docx":
            from docx import Document

            return "\n".join(p.text for p in Document(str(path)).paragraphs)
        if suffix in {".txt", ".md", ".py", ".c", ".h", ".java", ".csv", ".json"}:
            return path.read_text(errors="ignore")
        if suffix in {".html", ".htm"}:
            return html_to_text(path.read_text(errors="ignore"))
    except Exception as exc:  # one broken file shouldn't kill the whole sync
        return f"[could not extract text: {exc}]"
    return ""
