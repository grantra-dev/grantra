import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, vi } from "vitest";

// jsdom does not scroll.
window.scrollTo = () => {};
Element.prototype.scrollIntoView = () => {};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
