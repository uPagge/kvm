// Isolated browser boundary fixture: real useMouse and HID serializers, no device.
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import { rolldown } from "rolldown";
import { chromium } from "playwright";
const entry = fileURLToPath(new URL("./mouse-profile-entry.ts", import.meta.url));
const bundle = await rolldown({
  input: entry,
  transform: { define: { "process.env.NODE_ENV": '"production"' } },
  plugins: [
    {
      name: "mouse-boundary-fixture",
      resolveId(source) {
        if (source === entry) return entry;
        if (source === "./stores" || source === "@hooks/stores") return "\0stores";
        if (source === "./useJsonRpc") return "\0jsonrpc";
        if (source === "./useHidRpc") return "\0hidrpc";
      },
      load(id) {
        if (id === "\0stores")
          return `export const hidKeyBufferSize=6; export const useSettingsStore = () => window.settings; export const useMouseStore = () => window.mouseStore;`;
        if (id === "\0jsonrpc") return `export const useJsonRpc = () => ({send: window.send});`;
        if (id === "\0hidrpc") return `export const useHidRpc = () => window.transport;`;
        if (id === entry)
          return `
        import { createElement } from 'react';
        import { createRoot } from 'react-dom/client';
        import { flushSync } from 'react-dom';
        import useMouse from '../src/hooks/useMouse';
        import { MouseReportMessage } from '../src/hooks/hidRpc';
        window.encodeMove=(dx,dy,buttons)=>Array.from(new MouseReportMessage(dx,dy,buttons).marshal());
        const root = createRoot(document.getElementById('root'));
        function Harness() { window.mouse = useMouse(); return createElement('button', {onClick:()=>{}}, 'Mouse fixture'); }
        window.renderMouse = () => flushSync(() => root.render(createElement(Harness)));
        window.unmountMouse = () => flushSync(() => root.unmount());
      `;
      },
    },
  ],
});
const { output } = await bundle.generate({ format: "iife" });
await bundle.close();
const browser = await chromium.launch({ executablePath: process.env.CHROME_BIN, headless: true });
try {
  const page = await browser.newPage({ viewport: { width: 1000, height: 700 } });
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.setContent('<title>JetKVM isolated mouse fixture</title><div id="root"></div>');
  await page.evaluate(() => {
    window.reports = [];
    window.binaryReports = [];
    window.mouseStore = { setMousePosition() {}, setMouseMove() {} };
    window.settings = {
      mouseMode: "absolute",
      scrollThrottling: 0,
      invertScroll: false,
      setMouseMode(mode) {
        window.settings.mouseMode = mode;
      },
    };
    window.send = (method, params, callback) => {
      if (method === "getUsbDevices")
        callback({
          result: {
            hid_profile: "standard-relative",
            keyboard: true,
            relative_mouse: true,
            absolute_mouse: false,
            audio: false,
            mass_storage: false,
            serial_console: false,
          },
        });
      else window.reports.push({ method, params });
    };
    window.transport = {
      rpcHidReady: true,
      reportRelMouseEvent(dx, dy, buttons) {
        window.binaryReports.push(window.encodeMove(dx, dy, buttons));
        window.reports.push({ method: "relative", params: { dx, dy, buttons } });
      },
      reportAbsMouseEvent(x, y, buttons) {
        window.reports.push({ method: "absolute", params: { x, y, buttons } });
      },
    };
  });
  await page.addScriptTag({ content: output[0].code });
  await page.evaluate(() => window.renderMouse());
  await page.waitForTimeout(30);
  const motion = await page.evaluate(() => {
    window.mouse.getRelMouseMoveHandler()({ movementX: 300, movementY: -129, buttons: 31 });
    return window.reports.splice(0);
  });
  assert.deepEqual(motion, [
    { method: "relative", params: { dx: 127, dy: -127, buttons: 7 } },
    { method: "relative", params: { dx: 127, dy: -2, buttons: 7 } },
    { method: "relative", params: { dx: 46, dy: 0, buttons: 7 } },
  ]);
  assert.deepEqual(await page.evaluate(() => window.binaryReports.splice(0)), [
    [6, 127, 129, 7],
    [6, 127, 254, 7],
    [6, 46, 0, 7],
  ]);
  const small = await page.evaluate(() => {
    window.mouse.getRelMouseMoveHandler()({ movementX: 3, movementY: -2, buttons: 1 });
    window.mouse.getMouseWheelHandler()({ deltaY: 100, deltaX: 100 });
    window.dispatchEvent(new Event("blur"));
    document.dispatchEvent(new Event("pointerlockchange"));
    return window.reports.splice(0);
  });
  assert.deepEqual(small, [
    { method: "relative", params: { dx: 3, dy: -2, buttons: 1 } },
    { method: "wheelReport", params: { wheelY: -1, wheelX: 0 } },
    { method: "relative", params: { dx: 0, dy: 0, buttons: 0 } },
    { method: "relative", params: { dx: 0, dy: 0, buttons: 0 } },
  ]);
  const fractional = await page.evaluate(() => {
    window.mouse.getRelMouseMoveHandler()({ movementX: 0.4, movementY: 0, buttons: 0 });
    window.mouse.getRelMouseMoveHandler()({ movementX: 0.4, movementY: 0, buttons: 0 });
    window.mouse.getRelMouseMoveHandler()({ movementX: 0.4, movementY: 0, buttons: 0 });
    return window.reports.splice(0).map(report => report.params.dx);
  });
  assert.deepEqual(fractional, [0, 0, 1]);
  await page.evaluate(() => {
    window.transport.rpcHidReady = false;
    window.renderMouse();
  });
  const fallback = await page.evaluate(() => {
    window.reports = [];
    window.mouse.getRelMouseMoveHandler()({ movementX: 327, movementY: -255, buttons: 31 });
    return window.reports.splice(0);
  });
  assert.deepEqual(fallback, [
    { method: "relMouseReport", params: { dx: 127, dy: -127, buttons: 7 } },
    { method: "relMouseReport", params: { dx: 127, dy: -127, buttons: 7 } },
    { method: "relMouseReport", params: { dx: 73, dy: -1, buttons: 7 } },
  ]);
  await page.evaluate(() => window.unmountMouse());
  assert.deepEqual(await page.evaluate(() => window.reports.splice(0)), [
    { method: "relMouseReport", params: { dx: 0, dy: 0, buttons: 0 } },
  ]);
  assert.equal(await page.title(), "JetKVM isolated mouse fixture");
  assert.deepEqual(errors, []);
  console.log(
    "PASS: persisted absolute overridden; displacement split without acceleration on binary/JSON paths; literal HID bytes; fractional displacement retained; three buttons; vertical wheel; blur/lock-loss/unmount release; no browser errors",
  );
} finally {
  await browser.close();
}
