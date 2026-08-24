import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, vi } from "vitest";

afterEach(() => {
  cleanup();
  window.localStorage.clear();
  document.documentElement.dataset.theme = "light";
  document.documentElement.style.colorScheme = "";
});

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => undefined,
    removeListener: () => undefined,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    dispatchEvent: () => false
  })
});

class TestResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

vi.stubGlobal("ResizeObserver", TestResizeObserver);

const getComputedStyle = window.getComputedStyle.bind(window);
window.getComputedStyle = (element: Element) => getComputedStyle(element);
