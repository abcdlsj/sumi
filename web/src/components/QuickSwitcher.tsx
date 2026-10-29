import { Search } from "lucide-react";
import { type KeyboardEvent, type ReactNode, useState } from "react";

import { DialogFrame } from "./DialogFrame";

export interface QuickSwitcherItem {
  id: string;
  label: string;
  group: string;
  hint?: string;
  leading: ReactNode;
  href: string;
}

/** ⌘K: jump to any Channel, DM, Agent or Space tool by name. */
export function QuickSwitcher({
  items,
  close,
  open,
}: {
  items: QuickSwitcherItem[];
  close: () => void;
  open: (href: string) => void;
}) {
  const [query, setQuery] = useState("");
  const [activeIndex, setActiveIndex] = useState(0);
  const normalized = query.trim().toLocaleLowerCase();
  const matches = items.filter((item) => !normalized || item.label.toLocaleLowerCase().includes(normalized));
  const active = Math.min(activeIndex, Math.max(matches.length - 1, 0));

  function handleKey(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      if (!matches.length) return;
      const step = event.key === "ArrowDown" ? 1 : -1;
      setActiveIndex((active + step + matches.length) % matches.length);
    } else if (event.key === "Enter" && matches[active]) {
      event.preventDefault();
      open(matches[active].href);
    }
  }

  let previousGroup: string | undefined;
  return (
    <DialogFrame className="quick-switcher" close={close} labelId="quick-switcher-title">
      <h2 id="quick-switcher-title" className="visually-hidden">Jump to</h2>
      <label className="quick-switcher-search">
        <Search aria-hidden="true" />
        <input
          data-dialog-initial-focus
          role="combobox"
          aria-expanded="true"
          aria-controls="quick-switcher-results"
          aria-activedescendant={matches[active] ? `quick-switcher-${matches[active].id}` : undefined}
          aria-label="Jump to"
          placeholder="Jump to a Channel, DM or page"
          value={query}
          onChange={(event) => {
            setQuery(event.target.value);
            setActiveIndex(0);
          }}
          onKeyDown={handleKey}
        />
        <kbd>Esc</kbd>
      </label>
      <ul id="quick-switcher-results" className="quick-switcher-results" role="listbox" aria-label="Results">
        {matches.length ? matches.map((item, index) => {
          const heading = item.group !== previousGroup ? item.group : undefined;
          previousGroup = item.group;
          return (
            <li key={item.id} role="presentation">
              {heading ? <p className="quick-switcher-group" aria-hidden="true">{heading}</p> : null}
              <div
                id={`quick-switcher-${item.id}`}
                role="option"
                aria-selected={index === active}
                className="quick-switcher-option"
                onMouseMove={() => setActiveIndex(index)}
                onClick={() => open(item.href)}
              >
                <span className="quick-switcher-leading">{item.leading}</span>
                <span className="quick-switcher-label">{item.label}</span>
                {item.hint ? <small>{item.hint}</small> : null}
              </div>
            </li>
          );
        }) : <li className="quick-switcher-empty">No matches for “{query.trim()}”</li>}
      </ul>
    </DialogFrame>
  );
}
