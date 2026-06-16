import { useEffect, useState } from "react";
import axios from "axios";

type Entry = {
  workflow: string;
  provider: string;
  rh_mapped: boolean;
  media_type: string;
  label?: string;
};

let cache: Promise<Record<string, Record<string, Entry>>> | null = null;

function loadActiveWorkflows() {
  if (!cache) {
    cache = axios
      .get("/api/active-workflows")
      .then((r) => r.data?.by_section || {})
      .catch(() => ({}));
  }
  return cache;
}

// Call after changing provider/default-model settings so badges re-resolve.
export function refreshWorkflowBadges() {
  cache = null;
}

const providerLabel: Record<string, string> = {
  local: "本地 ComfyUI",
  runninghub: "RunningHub",
  jimeng: "即梦",
};

/**
 * Small debug badge showing which workflow a given generation action currently
 * uses (resolves provider-conditional choices via /api/active-workflows).
 * Highlights red when the active provider is RunningHub but the workflow has no
 * workflowId mapping (it would fail).
 */
export default function WorkflowBadge({
  section,
  media,
}: {
  section: string;
  media: "image" | "video" | "audio";
}) {
  const [entry, setEntry] = useState<Entry | null>(null);

  useEffect(() => {
    let alive = true;
    loadActiveWorkflows().then((bySection) => {
      if (alive) setEntry(bySection?.[section]?.[media] || null);
    });
    return () => {
      alive = false;
    };
  }, [section, media]);

  if (!entry) return null;

  const isRH = entry.provider === "runninghub";
  const warn = isRH && !entry.rh_mapped;
  const provText = providerLabel[entry.provider] || entry.provider;
  const tip = `${media === "video" ? "视频" : media === "audio" ? "音频" : "图片"}生成方式：${provText}${
    isRH ? (entry.rh_mapped ? "（已映射 workflowId）" : "（未映射 workflowId，会失败）") : ""
  }`;

  return (
    <span
      title={tip}
      className={`inline-flex items-center gap-1 rounded border px-2 py-0.5 text-xs ${
        warn
          ? "border-red-400 bg-red-50 text-red-600"
          : "border-border bg-muted/40 text-muted-foreground"
      }`}
    >
      <span className="opacity-70">工作流:</span>
      <code className="font-mono">{entry.workflow}</code>
      {isRH && <span className="opacity-70">· {entry.rh_mapped ? "RH" : "RH⚠"}</span>}
    </span>
  );
}
