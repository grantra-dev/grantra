import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Input } from "@/components/ui/field";
import { listIdentities, type Identity } from "@/lib/api";

/** Finds an active person by name or email, and hands back the one picked. */
export function PersonSearch({
  id,
  placeholder = "Search people",
  action = "Pick",
  skip = [],
  onPick,
}: {
  id: string;
  placeholder?: string;
  /** What each result's button says before the name, such as "Add". */
  action?: string;
  /** Ids not to offer, such as the ones already picked. */
  skip?: string[];
  onPick: (person: Identity) => void;
}) {
  const [q, setQ] = useState("");
  const text = q.trim();
  const found = useQuery({
    queryKey: ["identities", "search", text],
    queryFn: () => listIdentities({ q: text, status: "active", kind: "human" }),
    enabled: text.length >= 2,
  });
  return (
    <>
      <Input
        id={id}
        type="search"
        value={q}
        onChange={(e) => setQ(e.target.value)}
        placeholder={placeholder}
      />
      {found.data && (
        <ul className="flex flex-col">
          {found.data
            .filter((p) => !skip.includes(p.id))
            .slice(0, 8)
            .map((p) => (
              <li key={p.id}>
                <button
                  type="button"
                  className="text-sm text-brand-700 hover:underline"
                  onClick={() => {
                    onPick(p);
                    setQ("");
                  }}
                >
                  {action} {p.display_name}
                </button>
              </li>
            ))}
        </ul>
      )}
    </>
  );
}
