#!/usr/bin/env python3
from __future__ import annotations

import gzip
import html
import ipaddress
import json
import os
import re
import socket
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

BIND = os.getenv("ANTICORS_BIND", "127.0.0.1")
PORT = int(os.getenv("ANTICORS_PORT", "18088"))
TIMEOUT = float(os.getenv("ANTICORS_TIMEOUT", "20"))
MAX_BYTES = int(os.getenv("ANTICORS_MAX_BYTES", str(25 * 1024 * 1024)))
MAX_REDIRECTS = int(os.getenv("ANTICORS_MAX_REDIRECTS", "5"))
USER_AGENT = os.getenv("ANTICORS_USER_AGENT", "GPTAdminAntiCors/1.0")
PUBLIC_PREFIX = os.getenv("ANTICORS_PUBLIC_PREFIX", "/web/")

HOP_BY_HOP = {
    "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
    "te", "trailers", "transfer-encoding", "upgrade", "host", "content-length",
}
REQ_DENY = HOP_BY_HOP | {"accept-encoding"}
RESP_DENY = HOP_BY_HOP | {"content-encoding", "content-length", "content-security-policy", "content-security-policy-report-only", "x-frame-options"}
URL_ATTRS = ("href", "src", "action", "poster", "manifest", "data-src", "data-href")
SRCSET_ATTRS = ("srcset", "imagesrcset")
CSS_URL_RE = re.compile(r"url\(\s*(['\"]?)([^)'\"\s]+)\1\s*\)", re.I)
HTML_URL_RE = re.compile(r"\b(" + "|".join(re.escape(a) for a in URL_ATTRS + SRCSET_ATTRS) + r")\s*=\s*(['\"])(.*?)\2", re.I | re.S)
HTML_URL_UNQUOTED_RE = re.compile(r"\b(" + "|".join(re.escape(a) for a in URL_ATTRS + SRCSET_ATTRS) + r")\s*=\s*([^\s>]+)", re.I)
META_REFRESH_RE = re.compile(r"(<meta\b[^>]*http-equiv\s*=\s*(['\"])refresh\2[^>]*content\s*=\s*(['\"])(.*?)\3[^>]*>)", re.I | re.S)


def _json_error(handler: BaseHTTPRequestHandler, status: int, error: str, detail: str = "") -> None:
    body = json.dumps({"error": error, "detail": detail}, ensure_ascii=False).encode("utf-8")
    handler.send_response(status)
    _cors(handler)
    handler.send_header("Content-Type", "application/json; charset=utf-8")
    handler.send_header("Content-Length", str(len(body)))
    handler.end_headers()
    if handler.command != "HEAD":
        handler.wfile.write(body)


def _cors(handler: BaseHTTPRequestHandler) -> None:
    origin = handler.headers.get("Origin") or "*"
    handler.send_header("Access-Control-Allow-Origin", origin)
    handler.send_header("Vary", "Origin")
    handler.send_header("Access-Control-Allow-Methods", "GET,HEAD,POST,PUT,PATCH,DELETE,OPTIONS")
    handler.send_header("Access-Control-Allow-Headers", handler.headers.get("Access-Control-Request-Headers", "*"))
    handler.send_header("Access-Control-Expose-Headers", "*, Content-Length, Content-Type, Location")
    handler.send_header("Access-Control-Max-Age", "86400")


def _extract_url(path: str) -> str | None:
    if not path.startswith(PUBLIC_PREFIX):
        return None
    raw = path[len(PUBLIC_PREFIX):]
    return raw or None


def _is_public_ip(ip: ipaddress._BaseAddress) -> bool:
    return not (
        ip.is_private or ip.is_loopback or ip.is_link_local or ip.is_multicast
        or ip.is_reserved or ip.is_unspecified
    )


def _is_public_hostname(hostname: str) -> bool:
    if not hostname:
        return False
    h = hostname.rstrip(".").lower()
    if h in {"localhost", "localhost.localdomain"} or h.endswith(".local"):
        return False
    try:
        return _is_public_ip(ipaddress.ip_address(h.strip("[]")))
    except ValueError:
        pass
    try:
        infos = socket.getaddrinfo(h, None, type=socket.SOCK_STREAM)
    except socket.gaierror:
        raise ValueError("DNS resolution failed")
    addrs = {info[4][0] for info in infos}
    if not addrs:
        raise ValueError("DNS returned no addresses")
    for addr in addrs:
        try:
            if not _is_public_ip(ipaddress.ip_address(addr)):
                raise ValueError(f"blocked non-public address {addr}")
        except ValueError as e:
            if str(e).startswith("blocked"):
                raise
            raise ValueError(f"invalid resolved address {addr}")
    return True


def _validate_url(url: str) -> urllib.parse.ParseResult:
    try:
        parsed = urllib.parse.urlsplit(url)
    except Exception as e:
        raise ValueError(f"bad url: {e}")
    if parsed.scheme not in {"http", "https"}:
        raise ValueError("only http/https URLs are allowed")
    if not parsed.hostname:
        raise ValueError("URL must include hostname")
    if not _is_public_hostname(parsed.hostname):
        raise ValueError("blocked non-public host")
    return parsed


def _proxify_url(raw: str, base_url: str) -> str:
    raw = html.unescape(raw.strip())
    if not raw or raw.startswith(("#", "data:", "blob:", "mailto:", "tel:", "javascript:", "about:")):
        return raw
    absolute = urllib.parse.urljoin(base_url, raw)
    parsed = urllib.parse.urlsplit(absolute)
    if parsed.scheme not in {"http", "https"}:
        return raw
    return PUBLIC_PREFIX + absolute


def _rewrite_srcset(value: str, base_url: str) -> str:
    parts = []
    for chunk in value.split(','):
        item = chunk.strip()
        if not item:
            continue
        bits = item.split()
        if not bits:
            continue
        bits[0] = _proxify_url(bits[0], base_url)
        parts.append(' '.join(bits))
    return ', '.join(parts)


def _rewrite_css(text: str, base_url: str) -> str:
    def repl(m):
        quote = m.group(1) or ''
        url = _proxify_url(m.group(2), base_url)
        return f"url({quote}{url}{quote})"
    return CSS_URL_RE.sub(repl, text)


def _rewrite_html(text: str, base_url: str) -> str:
    text = _rewrite_css(text, base_url)

    def repl_attr(m):
        attr, quote, value = m.group(1), m.group(2), m.group(3)
        if attr.lower() in SRCSET_ATTRS:
            value = _rewrite_srcset(value, base_url)
        else:
            value = _proxify_url(value, base_url)
        return f'{attr}={quote}{html.escape(value, quote=True)}{quote}'

    text = HTML_URL_RE.sub(repl_attr, text)

    def repl_unquoted(m):
        attr, value = m.group(1), m.group(2)
        # Do not double-process already quoted attrs caught above.
        if value.startswith(("'", '"')):
            return m.group(0)
        if attr.lower() in SRCSET_ATTRS:
            value = _rewrite_srcset(value, base_url)
        else:
            value = _proxify_url(value, base_url)
        return f'{attr}="{html.escape(value, quote=True)}"'

    text = HTML_URL_UNQUOTED_RE.sub(repl_unquoted, text)

    def repl_meta(m):
        tag, quote, content_quote, content = m.group(1), m.group(2), m.group(3), m.group(4)
        cm = re.search(r"url\s*=\s*([^;]+)$", content, re.I)
        if not cm:
            return tag
        new_url = _proxify_url(cm.group(1).strip(), base_url)
        new_content = re.sub(r"url\s*=\s*([^;]+)$", "url=" + new_url, content, flags=re.I)
        return tag.replace(content, html.escape(new_content, quote=True), 1)

    return META_REFRESH_RE.sub(repl_meta, text)


def _maybe_rewrite_body(data: bytes, headers, final_url: str) -> tuple[bytes, str | None, bool]:
    ctype = headers.get("Content-Type", "")
    encoding = (headers.get("Content-Encoding") or "").lower()
    if encoding == "gzip":
        try:
            data = gzip.decompress(data)
        except Exception:
            return data, None, False
    lower = ctype.lower()
    if "text/html" in lower or "application/xhtml+xml" in lower:
        charset = _charset(ctype)
        text = data.decode(charset, errors="replace")
        return _rewrite_html(text, final_url).encode(charset, errors="xmlcharrefreplace"), ctype, True
    if "text/css" in lower:
        charset = _charset(ctype)
        text = data.decode(charset, errors="replace")
        return _rewrite_css(text, final_url).encode(charset, errors="xmlcharrefreplace"), ctype, True
    return data, None, False


def _charset(ctype: str) -> str:
    m = re.search(r"charset=([^;\s]+)", ctype, re.I)
    return (m.group(1).strip('"') if m else "utf-8")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


OPENER = urllib.request.build_opener(NoRedirect)


class Proxy(BaseHTTPRequestHandler):
    server_version = "GPTAdminAntiCors/1.1"

    def log_message(self, fmt: str, *args) -> None:
        sys.stderr.write("%s %s %s\n" % (time.strftime("%Y-%m-%dT%H:%M:%S%z"), self.client_address[0], fmt % args))

    def do_OPTIONS(self):
        self.send_response(204)
        _cors(self)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_GET(self): self._proxy()
    def do_HEAD(self): self._proxy()
    def do_POST(self): self._proxy()
    def do_PUT(self): self._proxy()
    def do_PATCH(self): self._proxy()
    def do_DELETE(self): self._proxy()

    def _proxy(self) -> None:
        target = _extract_url(self.path)
        if not target:
            return _json_error(self, 400, "bad_request", f"Use {PUBLIC_PREFIX}https://example.com/path")
        try:
            # Keep percent-encoded bytes intact. Some real URLs use legacy query
            # encodings like Windows-1251 (%CC%EE%E9...), and decoding the whole
            # target as UTF-8 breaks them before urllib can send the request.
            if target.startswith(("http://", "https://")):
                url = target
            else:
                url = urllib.parse.unquote(target, encoding="utf-8", errors="strict")
            _validate_url(url)
        except (ValueError, UnicodeDecodeError) as e:
            return _json_error(self, 400, "blocked_url", str(e))

        body = None
        if self.command in {"POST", "PUT", "PATCH", "DELETE"}:
            n = int(self.headers.get("Content-Length") or "0")
            if n > MAX_BYTES:
                return _json_error(self, 413, "request_too_large", f"max {MAX_BYTES} bytes")
            body = self.rfile.read(n) if n else None
        try:
            return self._fetch(self.command, url, body, MAX_REDIRECTS)
        except Exception as e:
            return _json_error(self, 502, "upstream_error", str(e)[:500])

    def _fetch(self, method: str, url: str, body: bytes | None, redirects_left: int):
        _validate_url(url)
        headers = {k: v for k, v in self.headers.items() if k.lower() not in REQ_DENY}
        headers.setdefault("User-Agent", USER_AGENT)
        headers.setdefault("Accept-Encoding", "identity")
        req = urllib.request.Request(url, data=body, headers=headers, method=method)
        try:
            with OPENER.open(req, timeout=TIMEOUT) as resp:
                data = resp.read(MAX_BYTES + 1)
                return self._send_response(resp.status, resp.headers, data, resp.geturl())
        except urllib.error.HTTPError as e:
            if e.code in {301, 302, 303, 307, 308} and e.headers.get("Location"):
                loc = urllib.parse.urljoin(url, e.headers["Location"])
                if redirects_left > 0:
                    _validate_url(loc)
                    next_method = "GET" if e.code in {301, 302, 303} and method not in {"GET", "HEAD"} else method
                    next_body = None if next_method != method else body
                    return self._fetch(next_method, loc, next_body, redirects_left - 1)
                data = e.read(MAX_BYTES + 1)
                return self._send_response(e.code, e.headers, data, url, rewrite_location=loc)
            data = e.read(MAX_BYTES + 1)
            return self._send_response(e.code, e.headers, data, url)

    def _send_response(self, status: int, headers, data: bytes, final_url: str, rewrite_location: str | None = None):
        if len(data) > MAX_BYTES:
            return _json_error(self, 502, "response_too_large", f"max {MAX_BYTES} bytes")
        data, rewritten_ctype, rewritten = _maybe_rewrite_body(data, headers, final_url)
        self.send_response(status)
        _cors(self)
        for k, v in headers.items():
            lk = k.lower()
            if lk in RESP_DENY:
                continue
            if lk == "location":
                v = _proxify_url(v, final_url)
            elif lk == "set-cookie":
                v = re.sub(r";\s*Domain=[^;]+", "", v, flags=re.I)
                v = re.sub(r";\s*Path=[^;]+", f"; Path={PUBLIC_PREFIX}", v, flags=re.I)
                if "path=" not in v.lower():
                    v += f"; Path={PUBLIC_PREFIX}"
            self.send_header(k, v)
        if rewrite_location:
            self.send_header("Location", _proxify_url(rewrite_location, final_url))
        if rewritten and rewritten_ctype:
            self.send_header("Content-Type", rewritten_ctype)
        self.send_header("X-AntiCors-Proxy", "whitetransport")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(data)


def main() -> None:
    httpd = ThreadingHTTPServer((BIND, PORT), Proxy)
    print(f"anticors listening on http://{BIND}:{PORT}{PUBLIC_PREFIX}<url>", flush=True)
    httpd.serve_forever()


if __name__ == "__main__":
    main()
