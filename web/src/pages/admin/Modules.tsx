import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Loaded } from "@/components/Loaded";
import { ErrorAlert } from "@/components/ui/alert";
import { listModules, setModule, type ConsoleModule } from "@/lib/console";
import { useCanManage } from "@/lib/manage";

/** Settings → Modules: the parts of Grantra this company shows in the console. */
export function Modules() {
  const { data, error, isPending } = useQuery({ queryKey: ["modules"], queryFn: listModules });
  return (
    <div className="flex flex-col gap-4">
      <Loaded loading={isPending} error={error}>
        <ul className="divide-y divide-slate-100">
          {data?.map((m) => (
            <ModuleRow key={m.id} m={m} />
          ))}
        </ul>
      </Loaded>
      <p className="text-sm text-slate-500">
        Identity governance (access, requests, reviews and the catalog) is always on. Turning a
        module off hides its pages, its search results and its tasks for everyone. It does not
        change anyone&apos;s access or stop the gateways: remove access, or turn the gateway off,
        for that.
      </p>
    </div>
  );
}

function ModuleRow({ m }: { m: ConsoleModule }) {
  const client = useQueryClient();
  const canManage = useCanManage();
  const toggle = useMutation({
    mutationFn: () => setModule(m.id, !m.enabled),
    onSuccess: async () => {
      // The menu, search and inbox all follow from these.
      await Promise.all([
        client.invalidateQueries({ queryKey: ["modules"] }),
        client.invalidateQueries({ queryKey: ["me"] }),
        client.invalidateQueries({ queryKey: ["inbox"] }),
      ]);
    },
  });
  const id = `module-${m.id}`;
  return (
    <li className="flex items-start justify-between gap-4 p-4">
      <div className="min-w-0">
        <label htmlFor={id} className="font-medium">
          {m.title}
        </label>
        <p className="text-sm text-slate-500">{m.description}</p>
        <ErrorAlert error={toggle.error} className="mt-2" />
      </div>
      <input
        id={id}
        type="checkbox"
        role="switch"
        className="mt-1 size-5 accent-brand-600"
        checked={m.enabled}
        disabled={!canManage || toggle.isPending}
        onChange={() => toggle.mutate()}
      />
    </li>
  );
}
