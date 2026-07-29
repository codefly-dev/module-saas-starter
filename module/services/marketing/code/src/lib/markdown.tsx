import type { ReactNode } from "react";

const LINK = /\[([^\]]+)\]\(([^)]+)\)/g;

function safeHref(value: string): string | null {
  if (value.startsWith("/") && !value.startsWith("//")) return value;
  try {
    const url = new URL(value);
    return url.protocol === "https:" || url.protocol === "mailto:" ? value : null;
  } catch {
    return null;
  }
}

function inline(text: string): ReactNode[] {
  const nodes: ReactNode[] = [];
  let cursor = 0;
  for (const match of text.matchAll(LINK)) {
    const index = match.index ?? 0;
    if (index > cursor) nodes.push(text.slice(cursor, index));
    const href = safeHref(match[2]);
    nodes.push(
      href ? (
        <a href={href} key={`${index}-${href}`}>
          {match[1]}
        </a>
      ) : (
        match[1]
      ),
    );
    cursor = index + match[0].length;
  }
  if (cursor < text.length) nodes.push(text.slice(cursor));
  return nodes;
}

export function Markdown({ source }: { source: string }) {
  const lines = source.split(/\r?\n/);
  const output: ReactNode[] = [];
  let index = 0;
  while (index < lines.length) {
    const line = lines[index];
    if (!line.trim()) {
      index += 1;
      continue;
    }
    if (line.startsWith("```")) {
      const language = line.slice(3).trim() || "text";
      const code: string[] = [];
      index += 1;
      while (index < lines.length && !lines[index].startsWith("```")) {
        code.push(lines[index]);
        index += 1;
      }
      index += 1;
      output.push(
        <figure className="code-example" key={`code-${index}`}>
          <figcaption>{language} example</figcaption>
          <pre tabIndex={0} aria-label={`${language} code example`}>
            <code>{code.join("\n")}</code>
          </pre>
        </figure>,
      );
      continue;
    }
    const heading = /^(#{2,4})\s+(.+)$/.exec(line);
    if (heading) {
      const level = heading[1].length;
      const id = heading[2]
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-|-$/g, "");
      const Heading = `h${level}` as "h2" | "h3" | "h4";
      output.push(
        <Heading id={id} key={`heading-${index}`}>
          {inline(heading[2])}
        </Heading>,
      );
      index += 1;
      continue;
    }
    if (line.startsWith("- ")) {
      const items: string[] = [];
      while (index < lines.length && lines[index].startsWith("- ")) {
        items.push(lines[index].slice(2));
        index += 1;
      }
      output.push(
        <ul key={`list-${index}`}>
          {items.map((item) => (
            <li key={item}>{inline(item)}</li>
          ))}
        </ul>,
      );
      continue;
    }
    const paragraph = [line];
    index += 1;
    while (
      index < lines.length &&
      lines[index].trim() &&
      !/^(#{2,4})\s|^- |^```/.test(lines[index])
    ) {
      paragraph.push(lines[index]);
      index += 1;
    }
    output.push(<p key={`paragraph-${index}`}>{inline(paragraph.join(" "))}</p>);
  }
  return <div className="prose">{output}</div>;
}
