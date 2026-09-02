// Tests for the generated product tile.
import { describe, expect, it } from "vitest";
import { monogramFor, productArt } from "./product-art";

describe("monogramFor", () => {
  it("takes the initials of the first two words", () => {
    expect(monogramFor("TrimPro Nova 5-in-1")).toBe("TN");
    expect(monogramFor("CalmSkin SPF 50 Daily")).toBe("CS");
  });

  it("skips digits and punctuation rather than showing them", () => {
    expect(monogramFor("9Lives 3D Rotary")).toBe("LD");
    expect(monogramFor("GlideShave Wet/Dry")).toBe("GW");
  });

  it("falls back to two letters of a single word", () => {
    expect(monogramFor("Trimmer")).toBe("TR");
  });

  it("says so rather than rendering an empty tile", () => {
    expect(monogramFor("")).toBe("??");
    expect(monogramFor("5-1")).toBe("??");
  });
});

describe("productArt", () => {
  it("gives every item in a category the same colour", () => {
    const one = productArt({ name: "TrimPro Nova", category: "trimmer" });
    const two = productArt({ name: "BladeMaster Pro 9", category: "trimmer" });
    expect(one.background).toBe(two.background);
    expect(one.monogram).not.toBe(two.monogram);
  });

  it("does not change colour between renders", () => {
    const first = productArt({ name: "CalmSkin Aloe", category: "cream" });
    const again = productArt({ name: "CalmSkin Aloe", category: "cream" });
    expect(again).toEqual(first);
  });

  it("separates the categories a shopper is comparing", () => {
    const trimmer = productArt({ name: "TrimPro Nova", category: "trimmer" });
    const cream = productArt({ name: "CalmSkin Aloe", category: "cream" });
    expect(trimmer.background).not.toBe(cream.background);
  });

  it("never puts a colour on itself", () => {
    for (const category of ["trimmer", "cream", "shaver", "beard_oil", "kit"]) {
      const art = productArt({ name: "Some Product", category });
      expect(art.foreground).not.toBe(art.background);
    }
  });
});
