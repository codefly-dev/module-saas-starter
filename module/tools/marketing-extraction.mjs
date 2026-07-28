import { spawn } from "node:child_process";
import { cp, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const toolsDirectory = path.dirname(fileURLToPath(import.meta.url));
const moduleDirectory = path.resolve(toolsDirectory, "..");
const source = path.join(moduleDirectory, "services", "marketing");
const temporaryRoot = await mkdtemp(path.join(tmpdir(), "marketing-extraction-"));
const target = path.join(temporaryRoot, "marketing");

function run(command, args, cwd, environment = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd,
      env: { ...process.env, ...environment },
      stdio: "inherit",
    });
    child.on("error", reject);
    child.on("exit", (code) => {
      if (code === 0) resolve();
      else reject(new Error(`${command} ${args.join(" ")} exited with ${code}`));
    });
  });
}

try {
  await cp(source, target, {
    recursive: true,
    filter: (entry) => ![".next", "node_modules"].includes(path.basename(entry)),
  });
  const code = path.join(target, "code");
  const environment = {
    MARKETING_CATALOG_URL: "http://localhost:9",
    MARKETING_ENABLED: "true",
    MARKETING_INDEXABLE: "false",
    MARKETING_STRICT_READINESS: "0",
  };
  await run("npm", ["ci", "--ignore-scripts"], code, environment);
  await run("npm", ["test"], code, environment);
  await run("npm", ["run", "quality"], code, environment);
  await run("npm", ["run", "build"], code, environment);
  await run("npm", ["run", "budget"], code, environment);
  await run("npm", ["run", "test:smoke"], code, environment);
  process.stdout.write("Isolated marketing extraction build passed.\n");
} finally {
  await rm(temporaryRoot, { recursive: true, force: true });
}
