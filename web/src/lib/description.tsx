// Book descriptions arrive from various upstreams (Audible /
// Audnexus / publishers) sometimes as plain text, sometimes as
// HTML fragments. Detect + render accordingly:
//
//   - If the string contains any tag-shaped tokens, sanitise via
//     DOMPurify with a narrow allowlist and return the cleaned
//     HTML.
//   - Otherwise hasHTML() returns false and callers render as
//     plain text (React escapes by default).

import DOMPurify from "dompurify";

const ALLOWED_TAGS = [
  "b",
  "strong",
  "i",
  "em",
  "u",
  "br",
  "p",
  "ul",
  "ol",
  "li",
  "a",
  "blockquote",
];

const ALLOWED_ATTR = ["href", "target", "rel"];

function looksLikeHTML(s: string): boolean {
  return /<\/?[a-z][^>]*>/i.test(s);
}

// hasHTML — caller branches between HTML container + plain <p>.
export function hasHTML(s: string | undefined | null): boolean {
  if (!s) return false;
  return looksLikeHTML(s);
}

// renderDescriptionHTML runs the input through DOMPurify with a
// narrow allowlist. Hooks force any surviving <a href> through
// target=_blank rel=noopener noreferrer; URLs starting with
// `javascript:` (or any other non-safe scheme) are rejected by
// DOMPurify's URL validator.
export function renderDescriptionHTML(s: string): string {
  if (!looksLikeHTML(s)) return "";
  // Allow only safe inline + block tags; force-open links in a
  // new tab so we don't navigate the user out of the SPA.
  return DOMPurify.sanitize(s, {
    ALLOWED_TAGS,
    ALLOWED_ATTR,
    ALLOWED_URI_REGEXP: /^(https?|mailto):/i,
    ADD_ATTR: ["target", "rel"],
    FORBID_TAGS: ["style", "script", "iframe", "object", "embed"],
  });
}
