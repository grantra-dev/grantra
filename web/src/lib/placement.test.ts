import { describe, expect, it } from "vitest";
import { placement } from "./placement";

const at = (left: number, top: number) => new DOMRect(left, top, 32, 32);

describe("placement", () => {
  it("opens below the ? with room for its content", () => {
    expect(placement(at(100, 100), "start", 1280, 900)).toEqual({
      left: 100,
      width: 416,
      top: 140,
      maxHeight: 744,
    });
  });

  it("opens above a ? near the bottom, no taller than the space there", () => {
    expect(placement(at(100, 800), "start", 1280, 900)).toEqual({
      left: 100,
      width: 416,
      bottom: 108,
      maxHeight: 776,
    });
  });

  it("stays inside the screen sideways", () => {
    expect(placement(at(1200, 100), "start", 1280, 900).left).toBe(1280 - 416 - 16);
    expect(placement(at(10, 100), "end", 1280, 900).left).toBe(16);
    // A phone: as wide as the screen allows.
    expect(placement(at(300, 100), "start", 390, 844)).toMatchObject({ left: 16, width: 358 });
  });
});
