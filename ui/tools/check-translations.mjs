import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

// Inlang can return exit 0 after plugin loading failed. Check the generated
// callable output, including a non-base locale, before accepting a UI build.
const output = process.argv[2]
  ? pathToFileURL(resolve(process.argv[2]))
  : new URL("../localization/paraglide/messages.js", import.meta.url);
const messages = await import(output.href);
assert.equal(typeof messages.loading, "function", "translation output has no callable loading message");

for (const locale of ["en", "ru"]) {
  const source = JSON.parse(
    await readFile(new URL(`../localization/messages/${locale}.json`, import.meta.url), "utf8"),
  );
  assert.equal(messages.loading({}, { locale }), source.loading, `${locale} output differs from its messages`);
}
console.log("Generated English/Russian translation smoke check passed.");
