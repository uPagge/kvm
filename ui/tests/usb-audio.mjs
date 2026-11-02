// Real USB settings component, controls and router; only RPC, stores and
// translations/notifications are isolated. No JetKVM/device connection.
import assert from "node:assert/strict";
import { existsSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { rolldown } from "rolldown";
import { chromium } from "playwright";

const entry = fileURLToPath(new URL("./usb-audio-entry.ts", import.meta.url));
const src = fileURLToPath(new URL("../src/", import.meta.url));
const bundle = await rolldown({
  input: entry,
  transform: {
    define: {
      "process.env.NODE_ENV": '"production"',
      "import.meta.env": "{}",
      "import.meta.hot": "false",
    },
  },
  plugins: [
    {
      name: "usb-audio-boundary",
      resolveId(source) {
        if (source === entry) return entry;
        if (source === "@hooks/useJsonRpc") return "\0rpc";
        if (source === "@hooks/stores") return "\0stores";
        if (source === "@/notifications") return "\0notifications";
        if (source === "@localizations/messages.js") return "\0messages";
        if (source.startsWith("@components/") || source.startsWith("@/")) {
          const base = src + source.replace(/^@components\//, "components/").replace(/^@\//, "");
          return [base, base + ".tsx", base + ".ts", base + "/index.ts", base + "/index.tsx"].find(
            path => existsSync(path) && statSync(path).isFile(),
          );
        }
      },
      load(id) {
        if (id === "\0rpc") return `export const useJsonRpc=()=>({send:window.send});`;
        if (id === "\0stores")
          return `export const useUiStore=()=>({setUsbSerialConsoleEnabled:window.setSerial}); export const useCapability=()=>true;`;
        if (id === "\0messages")
          return `export const m=new Proxy({}, {get:(_,key)=>()=>String(key)});`;
        if (id === "\0notifications")
          return `export default {success(message){window.notices.push({kind:'success',message});},error(message){window.notices.push({kind:'error',message});}};`;
        if (id === entry)
          return `
        import {createElement} from 'react'; import {createRoot} from 'react-dom/client';
        import {createMemoryRouter,RouterProvider} from 'react-router';
        import {UsbDeviceSetting} from '@components/UsbDeviceSetting';
        let root;
        window.mount=()=>{ root=createRoot(document.getElementById('root')); root.render(createElement(RouterProvider,{router:createMemoryRouter([{path:'/',element:createElement(UsbDeviceSetting)}])})); };
        window.unmount=()=>root.unmount();
      `;
      },
    },
  ],
});
const { output } = await bundle.generate({ format: "iife" });
await bundle.close();
const browser = await chromium.launch({ executablePath: process.env.CHROME_BIN, headless: true });
try {
  const page = await browser.newPage();
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.setContent('<title>USB audio isolated fixture</title><div id="root"></div>');
  await page.evaluate(() => {
    window.notices = [];
    window.mutations = [];
    window.failNext = false;
    window.pending = null;
    window.devices = {
      hid_profile: "standard-relative",
      keyboard: true,
      relative_mouse: true,
      absolute_mouse: false,
      mass_storage: false,
      serial_console: false,
      audio: false,
    };
    window.setSerial = () => {};
    window.send = (method, params, callback) => {
      if (method === "getUsbDevices") {
        callback({ result: { ...window.devices } });
        return;
      }
      window.mutations.push({ method, params });
      window.pending = () => {
        if (window.failNext) {
          window.failNext = false;
          callback({ error: { data: "rebind failed" } });
        } else {
          if (method === "setUsbDeviceState") window.devices.audio = params.enabled;
          callback({ result: null });
        }
        window.pending = null;
      };
    };
  });
  await page.addScriptTag({ content: output[0].code });
  await page.evaluate(() => window.mount());
  const audio = page.getByRole("checkbox");
  await audio.waitFor({ state: "visible", timeout: 3000 });
  assert.equal(await audio.count(), 1, "only optional audio should be editable");
  assert.equal(await audio.isChecked(), false);
  assert.equal(
    await page.getByRole("combobox").isDisabled(),
    true,
    "standard preset must stay locked",
  );
  assert.equal(await page.getByRole("button").count(), 0, "no preset apply/reset escape");
  await audio.click();
  assert.equal(
    await audio.isDisabled(),
    true,
    "prevent overlapping USB changes while re-enumerating",
  );
  assert.deepEqual(await page.evaluate(() => window.mutations), [
    { method: "setUsbDeviceState", params: { device: "audio", enabled: true } },
  ]);
  await page.evaluate(() => window.pending());
  await page.waitForFunction(
    () => !document.querySelector("input[type=checkbox]").matches(":disabled"),
  );
  assert.equal(await audio.isChecked(), true);
  // Remount reads the persisted response rather than retaining optimistic UI.
  await page.evaluate(() => {
    window.unmount();
    window.mount();
  });
  await audio.waitFor();
  await page.waitForFunction(() => document.querySelector("input[type=checkbox]")?.checked);
  await page.evaluate(() => {
    window.failNext = true;
  });
  await audio.click();
  await page.evaluate(() => window.pending());
  await page.waitForFunction(
    () => !document.querySelector("input[type=checkbox]").matches(":disabled"),
  );
  assert.equal(await audio.isChecked(), true, "failed toggle must restore acknowledged state");
  assert.equal(await page.evaluate(() => window.notices.at(-1).kind), "error");
  await audio.click();
  await page.evaluate(() => window.pending());
  await page.waitForFunction(
    () => !document.querySelector("input[type=checkbox]").matches(":disabled"),
  );
  assert.equal(await audio.isChecked(), false);
  await page.evaluate(() => {
    window.unmount();
    window.mount();
  });
  await audio.waitFor();
  await page.waitForFunction(
    () => !document.querySelector("input[type=checkbox]")?.matches(":disabled"),
  );
  assert.equal(await audio.isChecked(), false);
  assert.deepEqual(await page.evaluate(() => window.mutations), [
    { method: "setUsbDeviceState", params: { device: "audio", enabled: true } },
    { method: "setUsbDeviceState", params: { device: "audio", enabled: false } },
    { method: "setUsbDeviceState", params: { device: "audio", enabled: false } },
  ]);
  assert.deepEqual(errors, []);
  console.log(
    "PASS: real standard USB audio checkbox on/off/remount; exact existing RPC; error rollback; loading disabled; other preset/functions locked; no browser errors",
  );
} finally {
  await browser.close();
}
