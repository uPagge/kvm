// Real settings component and controls; only external RPC and display services
// are isolated. No device connection, foreground input or private browser state.
import assert from "node:assert/strict";
import { existsSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { rolldown } from "rolldown";
import { chromium } from "playwright";

// Independent contract literals: do not derive these from the component.
const neutral = {
  vendor_id: "0x1d6b",
  product_id: "0x0104",
  manufacturer: "",
  product: "USB Input Device",
  serial_number: "",
};
const custom = {
  vendor_id: "0x046d",
  product_id: "0xc534",
  manufacturer: "Logitech",
  product: "USB Receiver",
  serial_number: "",
};
const entry = fileURLToPath(new URL("./usb-identity-entry.ts", import.meta.url));
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
      name: "usb-identity-boundary",
      resolveId(source) {
        if (source === entry) return entry;
        if (source === "@hooks/useJsonRpc") return "\0rpc";
        if (source === "@/notifications") return "\0notifications";
        if (source === "@localizations/messages.js") return "\0messages";
        if (source === "@localizations/runtime.js") return "\0locale-runtime";
        if (source.startsWith("@components/") || source.startsWith("@/")) {
          const base = src + source.replace(/^@components\//, "components/").replace(/^@\//, "");
          return [base, base + ".tsx", base + ".ts", base + "/index.ts", base + "/index.tsx"].find(
            path => existsSync(path) && statSync(path).isFile(),
          );
        }
      },
      load(id) {
        if (id === "\0rpc") return "export const useJsonRpc=()=>({send:window.send});";
        if (id === "\0messages")
          return "export const m=new Proxy({}, {get:(_,key)=>()=>String(key)});";
        if (id === "\0locale-runtime")
          return "export const getLocale=()=> 'en'; export const locales=['en'];";
        if (id === "\0notifications")
          return "export default {success(message){window.notices.push({kind:'success',message});},error(message){window.notices.push({kind:'error',message});}};";
        if (id === entry)
          return `
        import {createElement} from 'react'; import {createRoot} from 'react-dom/client';
        import {createMemoryRouter,RouterProvider} from 'react-router';
        import {UsbInfoSetting} from '@components/UsbInfoSetting';
        let root;
        window.mount=()=>{root=createRoot(document.getElementById('root'));root.render(createElement(RouterProvider,{router:createMemoryRouter([{path:'/',element:createElement(UsbInfoSetting)}])}));};
        window.unmount=()=>root.unmount();
      `;
      },
    },
  ],
});
const { output } = await bundle.generate({ format: "iife" });
await bundle.close();
const browser = await chromium.launch({ executablePath: process.env.CHROME_BIN, headless: true });
const failures = [];
async function fixture(config, run) {
  const page = await browser.newPage();
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  page.on("console", message => {
    if (message.type() === "error") errors.push(message.text());
  });
  try {
    await page.setContent('<title>USB identity isolated fixture</title><div id="root"></div>');
    await page.evaluate(initial => {
      window.config = initial;
      window.notices = [];
      window.calls = [];
      window.mutations = [];
      window.pending = null;
      window.send = (method, params, callback) => {
        window.calls.push(method);
        if (method === "getUsbConfig") return callback({ result: { ...window.config } });
        if (method === "getDeviceID") return callback({ result: "fixture-device-id-do-not-emit" });
        if (method !== "setUsbConfig") throw new Error(`Unexpected RPC ${method}`);
        window.mutations.push({ method, params });
        window.pending = fail => {
          window.pending = null;
          if (fail) callback({ error: { data: "fixture rebind failed" } });
          else {
            window.config = { ...params.usbConfig };
            callback({ result: null });
          }
        };
      };
    }, config);
    await page.addScriptTag({ content: output[0].code });
    await page.evaluate(() => window.mount());
    await page.getByRole("combobox").waitFor();
    assert.equal(page.url(), "about:blank");
    assert.equal(await page.title(), "USB identity isolated fixture");
    await page.getByText("usb_config_identifiers_title", { exact: true }).waitFor();
    assert.equal(await page.locator("vite-error-overlay").count(), 0);
    await run(page);
    assert.deepEqual(errors, [], "no browser runtime or console errors");
  } finally {
    await page.close();
  }
}
async function remount(page) {
  await page.evaluate(() => {
    window.unmount();
    window.mount();
  });
  await page.getByRole("combobox").waitFor();
}
async function acknowledge(page, fail = false) {
  await page.evaluate(fail => window.pending(fail), fail);
  await page.waitForFunction(() => !document.querySelector("select").matches(":disabled"));
}
async function assertCustomFields(page, expected) {
  await page.waitForFunction(() => document.querySelector("select").value === "custom");
  assert.deepEqual(
    await page.locator("input").evaluateAll(inputs => inputs.map(input => input.value)),
    [
      expected.vendor_id,
      expected.product_id,
      expected.serial_number,
      expected.manufacturer,
      expected.product,
    ],
  );
}
const cases = [
  [
    "backend neutral identity selects Default on mount/remount",
    async () =>
      fixture(neutral, async page => {
        assert.equal(await page.getByRole("combobox").inputValue(), "USB Input Device");
        assert.equal(await page.locator("input").count(), 0);
        await remount(page);
        assert.equal(await page.getByRole("combobox").inputValue(), "USB Input Device");
        assert.deepEqual(await page.evaluate(() => window.mutations), []);
      }),
  ],
  [
    "Restore Default emits neutral literal and waits for ACK/refresh/remount",
    async () =>
      fixture(custom, async page => {
        await assertCustomFields(page, custom);
        await page.getByRole("button", { name: "usb_config_restore_default" }).click();
        assert.deepEqual(await page.evaluate(() => window.mutations), [
          { method: "setUsbConfig", params: { usbConfig: neutral } },
        ]);
        assert.equal(await page.getByRole("combobox").isDisabled(), true);
        assert.equal(
          await page.getByRole("button", { name: "usb_config_restore_default" }).isDisabled(),
          true,
        );
        assert.deepEqual(
          await page.evaluate(() => window.config),
          custom,
          "no optimistic persistence before ACK",
        );
        assert.deepEqual(await page.evaluate(() => window.notices), []);
        await page.evaluate(() => window.pending(false));
        assert.equal(
          await page.getByRole("combobox").isDisabled(),
          true,
          "existing post-ACK settle delay remains",
        );
        await page.waitForFunction(() => !document.querySelector("select").matches(":disabled"));
        assert.equal(await page.getByRole("combobox").inputValue(), "USB Input Device");
        assert.equal(await page.evaluate(() => window.notices.at(-1).kind), "success");
        assert.equal(
          await page.evaluate(
            () => window.calls.filter(method => method === "getUsbConfig").length,
          ),
          3,
          "initial parent/dialog and post-ACK refresh",
        );
        await remount(page);
        assert.equal(await page.getByRole("combobox").inputValue(), "USB Input Device");
      }),
  ],
  [
    "Default selector sends same neutral payload; failed ACK preserves Custom",
    async () =>
      fixture(custom, async page => {
        await assertCustomFields(page, custom);
        await page.getByRole("combobox").selectOption({ label: "usb_config_default" });
        assert.deepEqual(await page.evaluate(() => window.mutations), [
          { method: "setUsbConfig", params: { usbConfig: neutral } },
        ]);
        assert.equal(await page.getByRole("combobox").isDisabled(), true);
        await acknowledge(page, true);
        await assertCustomFields(page, custom);
        assert.equal(await page.evaluate(() => window.notices.at(-1).kind), "error");
        await remount(page);
        await assertCustomFields(page, custom);
      }),
  ],
  [
    "Custom owner identity and edited values remain unchanged by update/remount",
    async () =>
      fixture(custom, async page => {
        await assertCustomFields(page, custom);
        assert.deepEqual(await page.evaluate(() => window.mutations), []);
        await remount(page);
        await assertCustomFields(page, custom);
        await page.locator("input").nth(2).fill("owner-edited-serial");
        await page.getByRole("button", { name: "usb_config_update_identifiers" }).click();
        const edited = { ...custom, serial_number: "owner-edited-serial" };
        assert.deepEqual(await page.evaluate(() => window.mutations), [
          { method: "setUsbConfig", params: { usbConfig: edited } },
        ]);
        assert.equal(await page.getByRole("combobox").isDisabled(), true);
        await acknowledge(page);
        await remount(page);
        await assertCustomFields(page, edited);
      }),
  ],
];
try {
  for (const [name, run] of cases) {
    try {
      await run();
      console.log(`PASS: ${name}`);
    } catch (error) {
      failures.push(name);
      console.error(`FAIL: ${name}\n${error.stack}`);
    }
  }
} finally {
  await browser.close();
}
assert.deepEqual(failures, [], "USB identity behavior failures");
