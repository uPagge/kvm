// Real WebRTCVideo/useMouse integration; transport and unrelated panels are isolated.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { fileURLToPath } from "node:url";
import { rolldown } from "rolldown";
import { chromium } from "playwright";
const entry = fileURLToPath(new URL("./mouse-video-entry.ts", import.meta.url));
const mouse = fileURLToPath(new URL("../src/hooks/useMouse.ts", import.meta.url));
const video = fileURLToPath(new URL("../src/components/WebRTCVideo.tsx", import.meta.url));
const bundle = await rolldown({
  input: entry,
  transform: { define: { "process.env.NODE_ENV": '"production"' } },
  plugins: [
    {
      name: "video-mouse-fixture",
      resolveId(source) {
        if (source === entry) return entry;
        if (source === "@hooks/useMouse") return mouse;
        if (source === "@hooks/useDevicePixelRatio") return fileURLToPath(new URL("../src/hooks/useDevicePixelRatio.ts", import.meta.url));
        if (source === "@components/WebRTCVideo") return video;
        if (source === "./stores" || source === "@hooks/stores") return "\0stores";
        if (source === "./useJsonRpc" || source === "@hooks/useJsonRpc") return "\0jsonrpc";
        if (source === "./useHidRpc") return "\0hidrpc";
        if (source === "@hooks/useKeyboard") return "\0keyboard";
        if (source === "@/cva.config") return "\0cva";
        if (source === "@/utils") return "\0utils";
        if (source === "@/keyboardMappings") return "\0keys";
        if (source === "@/notifications") return "\0notifications";
        if (source === "@localizations/messages.js") return "\0messages";
        if (source === "@components/VideoOverlay") return "\0overlays";
        if (source.startsWith("@components/")) return "\0panel";
      },
      load(id) {
        if (id === "\0stores")
          return `export const useSettingsStore = () => window.settings; export const useMouseStore = () => window.mouseStore; export const useRTCStore = () => window.rtcStore; export const useVideoStore = () => window.videoStore; export const useUiStore=()=>({isOcrMode:false}); export const useCapability=()=>true; export const useUpdateStore=()=>({otaState:{updating:false,systemUpdatePending:false}});`;
        if (id === "\0jsonrpc") return `export const useJsonRpc = () => ({send: window.send});`;
        if (id === "\0hidrpc") return `export const useHidRpc = () => window.transport;`;
        if (id === "\0keyboard") return `export default () => window.keyboard;`;
        if (id === "\0cva")
          return `export const cx = (...args) => args.filter(x=>typeof x==='string').join(' ');`;
        if (id === "\0utils") return `export const isWindows=()=>false;`;
        if (id === "\0keys") return `export const keys={};`;
        if (id === "\0notifications")
          return `export default {success(){},error(message){ const alert=document.createElement('div'); alert.setAttribute('role','alert'); alert.textContent=message; document.body.append(alert); }};`;
        if (id === "\0messages")
          return `export const m = new Proxy({}, {get:(_,key)=>()=>String(key)});`;
        if (id === "\0overlays")
          return `export const HDMIErrorOverlay=()=>null,LoadingVideoOverlay=()=>null,NoAutoplayPermissionsOverlay=()=>null,PointerLockBar=()=>null,AudioPermissionBanner=()=>null,UpdateVideoPausedOverlay=()=>null;`;
        if (id === "\0panel") return `export default ()=>null;`;
        if (id === entry)
          return `
        import {createElement} from 'react'; import {createRoot} from 'react-dom/client';
        import WebRTCVideo from '@components/WebRTCVideo';
        const root=createRoot(document.getElementById('root'));
        root.render(createElement(WebRTCVideo,{hasConnectionIssues:false,hideStatusBar:true}));
        window.unmount=()=>root.unmount();
      `;
      },
    },
  ],
});
const { output } = await bundle.generate({ format: "iife" });
await bundle.close();
const server = createServer((req, res) => {
  res.setHeader("Content-Type", req.url === "/fixture.js" ? "application/javascript" : "text/html");
  res.end(
    req.url === "/fixture.js"
      ? output[0].code
      : '<!doctype html><title>JetKVM video mouse integration</title><style>video{width:800px;height:450px}</style><div id="root"></div><script src="/fixture.js"></script>',
  );
});
await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
const browser = await chromium.launch({
  executablePath: process.env.CHROME_BIN,
  headless: process.env.MOUSE_HEADED !== "1",
});
try {
  const page = await browser.newPage({ viewport: { width: 1000, height: 700 } });
  const errors = [];
  page.on("pageerror", e => errors.push(e.message));
  await page.addInitScript(
    ({ nativeLock, lockCase }) => {
      window.lockAttempts = [];
      const requestLock = Element.prototype.requestPointerLock;
      let fixtureLock = null;
      if (!nativeLock) {
        Object.defineProperty(document, "pointerLockElement", { get: () => fixtureLock });
        document.exitPointerLock = () => {
          fixtureLock = null;
          document.dispatchEvent(new Event("pointerlockchange"));
        };
      }
      Element.prototype.requestPointerLock =
        lockCase === "unsupported"
          ? undefined
          : function (...args) {
              window.lockAttempts.push({
                focus: document.hasFocus(),
                activation: navigator.userActivation.isActive,
              });
              if (lockCase === "rejected") return Promise.reject(new Error("Capture denied"));
              if (lockCase === "event-rejected") {
                setTimeout(() => document.dispatchEvent(new Event("pointerlockerror")), 0);
                return;
              }
              if (!nativeLock) {
                fixtureLock = this;
                document.dispatchEvent(new Event("pointerlockchange"));
                return Promise.resolve();
              }
              const result = requestLock.apply(this, args);
              result?.catch(error => window.lockAttempts.push({ error: error.message }));
              return result;
            };
      window.reports = [];
      window.settings = {
        mouseMode: "absolute",
        scrollThrottling: 0,
        invertScroll: false,
        setMouseMode(mode) {
          window.settings.mouseMode = mode;
        },
        videoSaturation: 1,
        videoBrightness: 1,
        videoContrast: 1,
      };
      window.mouseStore = { setMousePosition() {}, setMouseMove() {} };
      window.rtcStore = {
        mediaStream: null,
        mediaStreamTrackVersion: 0,
        peerConnectionState: "connected",
        peerConnection: { connectionState: "connected" },
      };
      window.videoStore = {
        width: 1920,
        height: 1080,
        clientWidth: 800,
        clientHeight: 450,
        hdmiState: "ready",
        setClientSize() {},
        setSize() {},
        setVideoElement() {},
        setContainerElement() {},
      };
      window.keyboard = { handleKeyPress() {}, resetKeyboardState() {} };
      window.send = (method, params, callback) => {
        if (method === "getUsbDevices") callback({ result: { hid_profile: "standard-relative" } });
        else if (method === "getAudioConfig") callback({ result: { enabled: false } });
        else window.reports.push({ method, params });
      };
      window.transport = {
        rpcHidReady: true,
        reportRelMouseEvent(dx, dy, buttons) {
          window.reports.push({ method: "relative", params: { dx, dy, buttons } });
        },
        reportAbsMouseEvent() {
          throw new Error("absolute report escaped standard profile");
        },
      };
    },
    { nativeLock: process.env.MOUSE_NATIVE_LOCK === "1", lockCase: process.env.MOUSE_LOCK_CASE },
  );
  // An insecure, non-localhost origin reproduces LAN HTTP without global security flags.
  await page.route("http://jetkvm.test/**", route =>
    route.fulfill({
      contentType: route.request().url().endsWith("/fixture.js")
        ? "application/javascript"
        : "text/html",
      body: route.request().url().endsWith("/fixture.js")
        ? output[0].code
        : '<!doctype html><title>JetKVM video mouse integration</title><style>video{width:800px;height:450px}</style><div id="root"></div><script src="/fixture.js"></script>',
    }),
  );
  await page.goto(
    process.env.MOUSE_NATIVE_LOCK === "1"
      ? `http://localhost:${server.address().port}/`
      : "http://jetkvm.test/",
  );
  if (process.env.MOUSE_NATIVE_LOCK !== "1")
    assert.equal(await page.evaluate(() => isSecureContext), false);
  await page.waitForSelector("video");
  await page.evaluate(() => document.querySelector("video").dispatchEvent(new Event("playing")));
  await page.waitForTimeout(30);
  await page.evaluate(() => {
    window.reports = [];
    document
      .querySelector("video")
      .dispatchEvent(new MouseEvent("mousemove", { movementX: 5, movementY: 4, buttons: 1 }));
  });
  assert.deepEqual(
    await page.evaluate(() => window.reports),
    [],
    "uncaptured input must stay local",
  );
  await page.locator("video").click();
  if (process.env.MOUSE_LOCK_CASE) {
    await page.getByRole("alert").waitFor({ timeout: 1500 });
    assert.equal(await page.getByRole("alert").textContent(), "video_pointer_lock_disabled");
    assert.equal(await page.evaluate(() => document.pointerLockElement), null);
    await page.evaluate(() =>
      document
        .querySelector("video")
        .dispatchEvent(new MouseEvent("mousemove", { movementX: 5, movementY: 4, buttons: 1 })),
    );
    assert.deepEqual(
      await page.evaluate(() => window.reports),
      [],
      "failed capture keeps input local",
    );
    assert.equal(
      await page.evaluate(() => window.lockAttempts.length),
      process.env.MOUSE_LOCK_CASE === "unsupported" ? 0 : 1,
    );
    assert.deepEqual(errors, []);
    console.log(
      `PASS: insecure HTTP ${process.env.MOUSE_LOCK_CASE} capture visibly fails and gates input`,
    );
  } else {
    try {
      await page.waitForFunction(
        () => document.pointerLockElement === document.querySelector("video"),
        null,
        { timeout: 1500 },
      );
    } catch (error) {
      console.log(
        await page.evaluate(() => ({
          lockAttempts: window.lockAttempts,
          mode: window.settings.mouseMode,
          focus: document.hasFocus(),
          url: location.href,
        })),
      );
      throw error;
    }
    assert.deepEqual(
      await page.evaluate(() => window.lockAttempts),
      [{ focus: true, activation: true }],
      "the component requests capture once from the trusted click",
    );
    await page.evaluate(() => {
      window.reports = [];
      document
        .querySelector("video")
        .dispatchEvent(
          new MouseEvent("mousemove", { movementX: 300, movementY: -129, buttons: 31 }),
        );
    });
    assert.deepEqual(await page.evaluate(() => window.reports.map(x => x.params)), [
      { dx: 127, dy: -127, buttons: 7 },
      { dx: 127, dy: -2, buttons: 7 },
      { dx: 46, dy: 0, buttons: 7 },
    ]);
    await page.evaluate(() => document.exitPointerLock());
    await page.waitForFunction(() => document.pointerLockElement === null);
    await page.waitForTimeout(20);
    assert.deepEqual(await page.evaluate(() => window.reports.at(-1)), {
      method: "relative",
      params: { dx: 0, dy: 0, buttons: 0 },
    });
    assert.equal(await page.title(), "JetKVM video mouse integration");
    assert.equal(await page.locator("video").count(), 1);
    assert.deepEqual(errors, []);
    await page.screenshot({
      path: process.env.MOUSE_SCREENSHOT || "/private/tmp/jetkvm-task3-mouse-video.png",
    });
    console.log(
      `PASS: real WebRTCVideo uncaptured input gate; trusted click requests ${process.env.MOUSE_NATIVE_LOCK === "1" ? "native" : "simulated browser-boundary"} pointer lock; bounded standard movement; exitPointerLock releases buttons; no page errors`,
    );
  }
} finally {
  await browser.close();
  await new Promise(resolve => server.close(resolve));
}
