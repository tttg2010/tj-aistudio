import { useEffect, useState } from "react";
import axios from "axios";
import { toast } from "sonner";
import { ChevronDown } from "lucide-react";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";

type Entry = {
  workflow: string;
  provider: string;
  rh_mapped: boolean;
  media_type: string;
  label?: string;
};

type WF = { workflow_name: string; type: string; file_name?: string };

let activeCache: Promise<Record<string, Record<string, Entry>>> | null = null;
let wfCache: Promise<WF[]> | null = null;

function loadActiveWorkflows() {
  if (!activeCache) {
    activeCache = axios
      .get("/api/active-workflows")
      .then((r) => r.data?.by_section || {})
      .catch(() => ({}));
  }
  return activeCache;
}

function loadWorkflowList() {
  if (!wfCache) {
    wfCache = axios
      .get("/api/workflows")
      .then((r) => (Array.isArray(r.data) ? r.data : []))
      .catch(() => []);
  }
  return wfCache;
}

// Call after changing provider/default-model/override settings so badges re-resolve.
export function refreshWorkflowBadges() {
  activeCache = null;
}

const providerLabel: Record<string, string> = {
  local: "本地 ComfyUI",
  runninghub: "RunningHub",
  jimeng: "即梦",
};

/**
 * Clickable badge showing (and letting you change) the workflow a given generate
 * action uses. Resolves provider-conditional choices and user overrides via
 * /api/active-workflows; saving an override hits /api/section-workflow and the
 * next generation uses the chosen workflow.
 */
export default function WorkflowBadge({
  section,
  media,
}: {
  section: string;
  media: "image" | "video" | "audio";
}) {
  const [entry, setEntry] = useState<Entry | null>(null);
  const [open, setOpen] = useState(false);
  const [options, setOptions] = useState<WF[]>([]);
  const [saving, setSaving] = useState(false);

  const reload = () =>
    loadActiveWorkflows().then((by) => setEntry(by?.[section]?.[media] || null));

  useEffect(() => {
    let alive = true;
    loadActiveWorkflows().then((by) => {
      if (alive) setEntry(by?.[section]?.[media] || null);
    });
    return () => {
      alive = false;
    };
  }, [section, media]);

  const openPicker = async () => {
    const wfs = await loadWorkflowList();
    // image→type "image", video→"video", audio→TTS/ASR workflows (parser marks them "unknown").
    const filtered = wfs.filter((w) =>
      media === "audio" ? w.type === "unknown" : w.type === media,
    );
    setOptions(filtered);
    setOpen(true);
  };

  const choose = async (file?: string) => {
    if (!file) return;
    setSaving(true);
    try {
      await axios.post("/api/section-workflow", { section, media, workflow: file });
      refreshWorkflowBadges();
      await reload();
      toast.success("已切换工作流：" + file);
      setOpen(false);
    } catch (e: any) {
      toast.error(e?.response?.data?.error || "切换失败");
    } finally {
      setSaving(false);
    }
  };

  if (!entry) return null;

  const isRH = entry.provider === "runninghub";
  const warn = isRH && !entry.rh_mapped;
  const mediaText = media === "video" ? "视频" : media === "audio" ? "音频" : "图片";

  // Only video workflows are safely swappable (node detection is automatic). Image
  // and audio inject by fixed node IDs, so switching to a structurally-different
  // workflow breaks generation — render those as a read-only label.
  const readonly = media !== "video";
  const badgeClass = `inline-flex items-center gap-1 rounded border px-2 py-0.5 text-xs ${
    warn ? "border-red-400 bg-red-50 text-red-600" : "border-border bg-muted/40 text-muted-foreground"
  }`;

  if (readonly) {
    const tip = `${mediaText}生成方式：${providerLabel[entry.provider] || entry.provider}${
      isRH ? (entry.rh_mapped ? "（已映射 workflowId）" : "（未映射 workflowId，会失败）") : ""
    }`;
    return (
      <span title={tip} className={badgeClass}>
        <span className="opacity-70">工作流:</span>
        <code className="font-mono">{entry.workflow}</code>
        {isRH && <span className="opacity-70">· {entry.rh_mapped ? "RH" : "RH⚠"}</span>}
      </span>
    );
  }

  const tip = `${mediaText}生成方式：${providerLabel[entry.provider] || entry.provider}${
    isRH ? (entry.rh_mapped ? "（已映射 workflowId）" : "（未映射 workflowId，会失败）") : ""
  } · 点击更换工作流`;

  return (
    <Popover open={open} onOpenChange={(o) => (o ? openPicker() : setOpen(false))}>
      <PopoverTrigger asChild>
        <button
          title={tip}
          className={`inline-flex items-center gap-1 rounded border px-2 py-0.5 text-xs transition-colors hover:ring-1 hover:ring-primary ${
            warn
              ? "border-red-400 bg-red-50 text-red-600"
              : "border-border bg-muted/40 text-muted-foreground"
          }`}
        >
          <span className="opacity-70">工作流:</span>
          <code className="font-mono">{entry.workflow}</code>
          {isRH && <span className="opacity-70">· {entry.rh_mapped ? "RH" : "RH⚠"}</span>}
          <ChevronDown className="h-3 w-3 opacity-60" />
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className="max-h-80 w-80 overflow-auto p-1">
        <div className="px-2 py-1 text-xs text-muted-foreground">
          选择{mediaText}工作流（{providerLabel[entry.provider] || entry.provider}）
        </div>
        {section !== "short_drama" && (
          <button
            disabled={saving}
            onClick={() => choose("__default__")}
            className="flex w-full flex-col rounded px-2 py-1.5 text-left text-xs hover:bg-accent disabled:opacity-50"
          >
            <span className="font-medium">恢复默认</span>
            <span className="text-[10px] text-muted-foreground">按生成方式自动选（本地/RunningHub）</span>
          </button>
        )}
        {options.length === 0 && (
          <div className="px-2 py-2 text-xs text-muted-foreground">无可用工作流</div>
        )}
        {options.map((w) => {
          const fn = w.file_name || "";
          const active = fn === entry.workflow;
          return (
            <button
              key={fn}
              disabled={saving || !fn}
              onClick={() => choose(fn)}
              className={`flex w-full flex-col rounded px-2 py-1.5 text-left text-xs hover:bg-accent disabled:opacity-50 ${
                active ? "bg-primary/10" : ""
              }`}
            >
              <span className="truncate font-mono">{fn}</span>
              <span className="truncate text-[10px] text-muted-foreground">
                {w.workflow_name}
                {active ? " · 当前" : ""}
              </span>
            </button>
          );
        })}
        {media !== "video" && (
          <div className="px-2 py-1.5 text-[10px] text-amber-600">
            注意：图片/音频工作流的参数按固定节点 ID 注入，更换后需保证新工作流节点结构兼容，否则会生成失败。
          </div>
        )}
      </PopoverContent>
    </Popover>
  );
}
