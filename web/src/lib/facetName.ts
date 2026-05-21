// titleFromFacetSlug un-slugs a URL id (e.g. "andy-weir" → "Andy Weir") for
// display purposes. Used as a fallback heading on Author/Series/Narrator
// detail pages while the first search result hasn't arrived (or when the
// facet has no books and there's nothing to derive a canonical name from).
export function titleFromFacetSlug(slug: string): string {
  if (!slug) return '';
  return slug
    .split(/[-_]+/)
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(' ');
}
