// Draws a product tile when there is no photograph, so a catalog reads as a shop
// rather than as a list. Colours come from the existing palette and are chosen by
// category, so every item in a category looks like it belongs to that category.
export type ProductArt = {
  monogram: string;
  background: string;
  foreground: string;
};

// The five colours the rest of the site already uses. Nothing new enters the
// design here: these are paired for contrast, not picked for variety.
const palettes: Array<{ background: string; foreground: string }> = [
  { background: "#dce9df", foreground: "#335c45" },
  { background: "#335c45", foreground: "#dce9df" },
  { background: "#c9654a", foreground: "#f7f7f2" },
  { background: "#15201b", foreground: "#dce9df" },
  { background: "#f7f7f2", foreground: "#335c45" },
];

// stableIndex maps a string to the same palette every time, so a product does not
// change colour between renders or between pages.
function stableIndex(key: string, buckets: number): number {
  let hash = 0;
  for (const character of key) {
    hash = (hash * 31 + (character.codePointAt(0) ?? 0)) % 1000003;
  }
  return hash % buckets;
}

// monogramFor takes the initials of the first two words, falling back to the
// first two letters of a single word. Digits and punctuation are skipped, so
// "TrimPro Nova 5-in-1" reads as TN rather than as T5.
export function monogramFor(name: string): string {
  const words = name
    .split(/[^A-Za-z]+/)
    .filter((word) => word.length > 0)
    .slice(0, 2);
  if (words.length === 0) {
    return "??";
  }
  if (words.length === 1) {
    return words[0].slice(0, 2).toUpperCase();
  }
  return words.map((word) => word[0]).join("").toUpperCase();
}

// productArt returns the tile for one product.
export function productArt(product: {
  name: string;
  category: string;
}): ProductArt {
  const palette = palettes[stableIndex(product.category, palettes.length)];
  return {
    monogram: monogramFor(product.name),
    background: palette.background,
    foreground: palette.foreground,
  };
}
