#!/usr/bin/env python3
"""Validate operator-provided branding assets without third-party libraries."""

import re
import xml.etree.ElementTree as ET
from pathlib import Path


MAX_LOGO_BYTES = 2 * 1024 * 1024
SUPPORTED_LOGO_TYPES = {
    "image/png": ".png",
    "image/jpeg": ".jpg",
    "image/gif": ".gif",
    "image/webp": ".webp",
    "image/svg+xml": ".svg",
}
BLOCKED_SVG_ELEMENTS = {
    "animate",
    "animatemotion",
    "animatetransform",
    "audio",
    "embed",
    "feimage",
    "foreignobject",
    "iframe",
    "image",
    "object",
    "script",
    "set",
    "style",
    "video",
}


def _local_name(value):
    return str(value).rsplit("}", 1)[-1].lower()


def _sniff_content_type(content):
    if content.startswith(b"\x89PNG\r\n\x1a\n"):
        return "image/png"
    if content.startswith(b"\xff\xd8\xff"):
        return "image/jpeg"
    if content.startswith((b"GIF87a", b"GIF89a")):
        return "image/gif"
    if len(content) >= 12 and content[:4] == b"RIFF" and content[8:12] == b"WEBP":
        return "image/webp"
    prefix = content[:512].lstrip(b"\xef\xbb\xbf\x00\t\r\n ").lower()
    if prefix.startswith(b"<svg") or (prefix.startswith(b"<?xml") and b"<svg" in prefix):
        return "image/svg+xml"
    return ""


def _validate_svg(content):
    lowered = content.lower()
    if b"<!doctype" in lowered or b"<!entity" in lowered:
        raise ValueError("SVG Logo 不能包含 DOCTYPE 或实体声明")
    try:
        root = ET.fromstring(content)
    except (ET.ParseError, ValueError) as error:
        raise ValueError("SVG Logo 不是有效 XML：{}".format(error))
    if _local_name(root.tag) != "svg":
        raise ValueError("SVG Logo 的根元素必须是 svg")
    element_count = 0
    for element in root.iter():
        element_count += 1
        if element_count > 5000:
            raise ValueError("SVG Logo 元素数量过多")
        if _local_name(element.tag) in BLOCKED_SVG_ELEMENTS:
            raise ValueError("SVG Logo 包含不允许的元素：{}".format(_local_name(element.tag)))
        for raw_name, raw_value in element.attrib.items():
            name = _local_name(raw_name)
            value = str(raw_value or "").strip()
            lowered_value = value.lower()
            if name.startswith("on"):
                raise ValueError("SVG Logo 不能包含事件处理属性")
            if name in ("href", "src") and value and not value.startswith("#"):
                raise ValueError("SVG Logo 不能引用外部资源")
            if name == "style" and re.search(r"(?:url\s*\(|@import|expression\s*\(|javascript:)", lowered_value):
                raise ValueError("SVG Logo 样式不能引用外部资源或脚本")
            for target in re.findall(r"url\s*\(\s*['\"]?([^)'\"\s]+)", lowered_value):
                if not target.startswith("#"):
                    raise ValueError("SVG Logo 不能通过属性引用外部资源")
            if "javascript:" in lowered_value or "data:text/html" in lowered_value:
                raise ValueError("SVG Logo 包含不安全的 URL")


def validate_logo(filename, declared_content_type, content):
    """Return normalized metadata for a supported and safely displayable logo."""
    name = Path(str(filename or "logo")).name
    if not name or len(name) > 128 or any(ord(character) < 32 for character in name):
        raise ValueError("Logo 文件名无效")
    content = bytes(content)
    if not content:
        raise ValueError("Logo 文件为空")
    if len(content) > MAX_LOGO_BYTES:
        raise ValueError("Logo 文件不能超过 2 MiB")
    detected = _sniff_content_type(content)
    if detected not in SUPPORTED_LOGO_TYPES:
        raise ValueError("Logo 仅支持 PNG、JPEG、GIF、WebP 和 SVG")
    declared = str(declared_content_type or "").split(";", 1)[0].strip().lower()
    if declared and declared not in (detected, "application/octet-stream"):
        raise ValueError("Logo 声明类型与文件内容不一致")
    if detected == "image/svg+xml":
        _validate_svg(content)
    stem = re.sub(r"[^A-Za-z0-9._-]+", "-", Path(name).stem).strip(".-") or "logo"
    return {
        "filename": stem[:96] + SUPPORTED_LOGO_TYPES[detected],
        "content_type": detected,
        "content": content,
    }
