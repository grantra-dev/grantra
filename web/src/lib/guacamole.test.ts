import { describe, expect, it } from "vitest";
import { Guacamole } from "./guacamole";

describe("Guacamole", () => {
  it("loads the client from the package's default export", () => {
    expect(typeof Guacamole.Client).toBe("function");
    expect(typeof Guacamole.WebSocketTunnel).toBe("function");
    expect(typeof Guacamole.SessionRecording).toBe("function");
  });
});
