import { createReadStream } from "node:fs";
import { readdir, stat } from "node:fs/promises";
import { createGzip } from "node:zlib";
import { pipeline } from "node:stream/promises";
import { Writable } from "node:stream";
import path from "node:path";

const staticRoot = path.join(process.cwd(), ".next", "static");
const publicRoot = path.join(process.cwd(), "public");
const budgets = {
  compressedJavaScriptAndCSS: 220 * 1024,
  rawCSS: 24 * 1024,
  socialImage: 400 * 1024,
};

async function files(directory) {
  const output = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const entryPath = path.join(directory, entry.name);
    if (entry.isDirectory()) output.push(...(await files(entryPath)));
    else output.push(entryPath);
  }
  return output;
}

async function gzipSize(file) {
  let size = 0;
  await pipeline(
    createReadStream(file),
    createGzip({ level: 9 }),
    new Writable({
      write(chunk, _encoding, callback) {
        size += chunk.length;
        callback();
      },
    }),
  );
  return size;
}

const assets = await files(staticRoot);
const scriptsAndStyles = assets.filter((file) => /\.(?:js|css)$/.test(file));
const styles = assets.filter((file) => file.endsWith(".css"));
const compressedJavaScriptAndCSS = (
  await Promise.all(scriptsAndStyles.map(gzipSize))
).reduce((sum, size) => sum + size, 0);
const rawCSS = (
  await Promise.all(styles.map(async (file) => (await stat(file)).size))
).reduce((sum, size) => sum + size, 0);
const socialImage = (await stat(path.join(publicRoot, "og.png"))).size;
const actual = { compressedJavaScriptAndCSS, rawCSS, socialImage };
const failures = Object.entries(actual).filter(
  ([name, value]) => value > budgets[name],
);

process.stdout.write(`${JSON.stringify({ budgets, actual }, null, 2)}\n`);
if (failures.length > 0) {
  throw new Error(
    `Marketing performance budgets exceeded:\n${failures
      .map(([name, value]) => `- ${name}: ${value} > ${budgets[name]}`)
      .join("\n")}`,
  );
}
