import { describe, expect, it } from "vitest";
import { capitalize, humanize } from "./utils";

describe("labels", () => {
  it("turns identifiers into words", () => {
    expect(humanize("platform_role.granted")).toBe("Platform role granted");
    expect(humanize("previous_country")).toBe("Previous country");
    expect(capitalize("critical")).toBe("Critical");
    expect(capitalize("")).toBe("");
  });
});
