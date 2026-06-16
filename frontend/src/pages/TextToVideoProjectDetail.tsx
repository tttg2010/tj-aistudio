import { useEffect, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import axios from "axios";
import { ArrowLeft, ChevronDown, Download, Play, RefreshCw, Save, Wand2 } from "lucide-react";
import { toast } from "sonner";

import type { TextToVideoLine, TextToVideoProject } from "@/types";
import { Textarea } from "@/components/ui/textarea";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "@/components/ui/dialog";
import WorkflowBadge from "@/components/WorkflowBadge";

const statusLabel: Record<string, string> = {
  draft: "未生成",
  generating: "生成中",
  generated: "已生成",
  failed: "失败",
};

const extractDownloadFilename = (cd: string | undefined, fallback: string) => {
  if (!cd) return fallback;
  const m = /filename\*?=(?:UTF-8'')?"?([^";]+)"?/i.exec(cd);
  if (m && m[1]) {
    try { return decodeURIComponent(m[1]); } catch { return m[1]; }
  }
  return fallback;
};

const triggerBlobDownload = (blob: Blob, filename: string) => {
  const url = window.URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  window.URL.revokeObjectURL(url);
};

export default function TextToVideoProjectDetail() {
  const { id } = useParams();
  const navigate = useNavigate();
  const [project, setProject] = useState<TextToVideoProject | null>(null);
  const [lines, setLines] = useState<TextToVideoLine[]>([]);
  const [text, setText] = useState("");
  const [savingText, setSavingText] = useState(false);
  const [generating, setGenerating] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [genMenuOpen, setGenMenuOpen] = useState(false);
  const [preview, setPreview] = useState<string | null>(null);
  const pollRef = useRef<number | null>(null);

  const fetchAll = async () => {
    if (!id) return;
    try {
      const [p, l] = await Promise.all([
        axios.get(`/api/text-to-video-projects/${id}`),
        axios.get(`/api/text-to-video-projects/${id}/lines`),
      ]);
      setProject(p.data);
      setText((prev) => (prev ? prev : p.data?.text || ""));
      setLines(Array.isArray(l.data) ? l.data : []);
    } catch {
      toast.error("读取文生视频项目失败");
    }
  };

  useEffect(() => {
    void fetchAll();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  // Poll while any line is generating.
  useEffect(() => {
    const anyGenerating = lines.some((l) => l.status === "generating");
    if (anyGenerating && pollRef.current === null) {
      pollRef.current = window.setInterval(async () => {
        if (!id) return;
        try {
          const l = await axios.get(`/api/text-to-video-projects/${id}/lines`);
          setLines(Array.isArray(l.data) ? l.data : []);
        } catch { /* ignore */ }
      }, 4000);
    } else if (!anyGenerating && pollRef.current !== null) {
      window.clearInterval(pollRef.current);
      pollRef.current = null;
    }
    return () => {
      if (!anyGenerating && pollRef.current !== null) {
        window.clearInterval(pollRef.current);
        pollRef.current = null;
      }
    };
  }, [lines, id]);

  const saveLines = async () => {
    if (!id) return;
    setSavingText(true);
    try {
      const res = await axios.post(`/api/text-to-video-projects/${id}/save-lines`, { text });
      setLines(Array.isArray(res.data?.lines) ? res.data.lines : []);
      toast.success("提示词已保存");
    } catch (err: any) {
      toast.error(err.response?.data?.error || "保存失败");
    } finally {
      setSavingText(false);
    }
  };

  const generateAll = async (randomSeed: boolean) => {
    if (!id) return;
    setGenMenuOpen(false);
    setGenerating(true);
    try {
      const res = await axios.post(`/api/text-to-video-projects/${id}/generate-lines`, { text, random_seed: randomSeed });
      toast.success(res.data?.message || "生成任务已提交");
      await fetchAll();
    } catch (err: any) {
      toast.error(err.response?.data?.error || "提交生成失败");
    } finally {
      setGenerating(false);
    }
  };

  const regenerateLine = async (line: TextToVideoLine, randomSeed: boolean) => {
    try {
      await axios.post(`/api/text-to-video-lines/${line.id}/generate`, { random_seed: randomSeed });
      toast.success("单段任务已提交");
      await fetchAll();
    } catch (err: any) {
      toast.error(err.response?.data?.error || "提交失败");
    }
  };

  const exportArchive = async () => {
    if (!project) return;
    setExporting(true);
    try {
      const res = await axios.post(`/api/text-to-video-projects/${project.id}/export`, {}, { responseType: "blob" });
      triggerBlobDownload(res.data, extractDownloadFilename(res.headers["content-disposition"], `${project.code || "text_to_video"}_export.zip`));
      toast.success("导出压缩包已开始下载");
    } catch (err: any) {
      let message = "导出失败";
      if (err?.response?.data instanceof Blob) {
        try { message = JSON.parse(await err.response.data.text())?.error || message; } catch { /* ignore */ }
      } else if (err?.response?.data?.error) {
        message = err.response.data.error;
      }
      toast.error(message);
    } finally {
      setExporting(false);
    }
  };

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <button onClick={() => navigate("/text-to-video")} className="mb-2 flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
            <ArrowLeft className="h-4 w-4" />
            返回 文生视频
          </button>
          <h1 className="truncate text-3xl font-bold">{project?.name || "文生视频"}</h1>
          <p className="mt-1 text-sm text-muted-foreground">文件名：{project?.code}</p>
          <div className="mt-2 flex flex-wrap gap-2">
            <WorkflowBadge section="text_to_video" media="video" />
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <button onClick={fetchAll} className="rounded-md border px-3 py-2 text-sm hover:bg-muted">
            <RefreshCw className="mr-1 inline h-4 w-4" />刷新
          </button>
          <button onClick={exportArchive} disabled={exporting} className="rounded-md border px-3 py-2 text-sm hover:bg-muted disabled:opacity-60">
            <Download className="mr-1 inline h-4 w-4" />{exporting ? "导出中..." : "导出"}
          </button>
          <Popover open={genMenuOpen} onOpenChange={setGenMenuOpen}>
            <PopoverTrigger asChild>
              <button disabled={generating} className="rounded-md bg-primary px-4 py-2 text-sm text-primary-foreground disabled:opacity-60">
                <Wand2 className="mr-1 inline h-4 w-4" />{generating ? "提交中..." : "生成全部"}<ChevronDown className="ml-1 inline h-4 w-4" />
              </button>
            </PopoverTrigger>
            <PopoverContent align="end" className="w-56 p-2">
              <button type="button" onClick={() => void generateAll(false)} className="flex w-full flex-col rounded-md px-3 py-2 text-left text-sm hover:bg-accent">
                <span className="font-medium">使用系统 seed</span>
                <span className="text-xs text-muted-foreground">读取系统设置里的全局 Seed。</span>
              </button>
              <button type="button" onClick={() => void generateAll(true)} className="mt-1 flex w-full flex-col rounded-md px-3 py-2 text-left text-sm hover:bg-accent">
                <span className="font-medium">随机 seed 抽卡</span>
                <span className="text-xs text-muted-foreground">每段都用新的随机 Seed。</span>
              </button>
            </PopoverContent>
          </Popover>
        </div>
      </div>

      <section className="rounded-xl border bg-card p-4 shadow-sm">
        <div className="mb-2 flex items-center justify-between">
          <h2 className="text-lg font-semibold">提示词（每段一个视频，空行分隔）</h2>
          <button onClick={saveLines} disabled={savingText} className="rounded-md border px-3 py-2 text-sm hover:bg-muted disabled:opacity-60">
            <Save className="mr-1 inline h-4 w-4" />{savingText ? "保存中..." : "保存提示词"}
          </button>
        </div>
        <Textarea rows={8} value={text} onChange={(e) => setText(e.target.value)} placeholder={"9:16 镜头缓慢推进，厨房，自然光...\n\n下一段提示词..."} />
      </section>

      <section className="space-y-3">
        {lines.map((line) => (
          <div key={line.id} className="rounded-xl border bg-card p-4 shadow-sm">
            <div className="flex items-start gap-4">
              <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full border bg-muted/40 text-sm font-medium">{line.sort_order}</div>
              <div className="min-w-0 flex-1">
                <p className="whitespace-pre-wrap break-words text-sm">{line.prompt}</p>
                <div className="mt-2 flex flex-wrap items-center gap-3 text-xs">
                  <span className={
                    line.status === "generated" ? "text-green-600" :
                    line.status === "failed" ? "text-red-600" :
                    line.status === "generating" ? "text-blue-600" : "text-muted-foreground"
                  }>
                    {statusLabel[line.status] || line.status}
                  </span>
                  {line.last_error ? <span className="text-red-600">· {line.last_error}</span> : null}
                </div>
              </div>
              <div className="flex shrink-0 flex-col items-end gap-2">
                {line.generated_video ? (
                  <div className="relative h-20 w-20 cursor-zoom-in" onClick={() => setPreview(line.generated_video)} title="点击播放">
                    <video src={line.generated_video} muted playsInline preload="metadata" className="h-20 w-20 rounded object-cover border bg-black/5 hover:ring-2 hover:ring-primary" />
                    <Play className="absolute inset-0 m-auto h-6 w-6 text-white drop-shadow" />
                  </div>
                ) : null}
                <div className="flex gap-2">
                  {line.generated_video ? (
                    <a href={line.generated_video} download className="rounded-md border px-2 py-1 text-xs hover:bg-muted">
                      <Download className="mr-1 inline h-3 w-3" />下载
                    </a>
                  ) : null}
                  <button onClick={() => regenerateLine(line, false)} disabled={line.status === "generating"} className="rounded-md border px-2 py-1 text-xs hover:bg-muted disabled:opacity-50">
                    {line.status === "generating" ? "生成中" : "生成"}
                  </button>
                  <button onClick={() => regenerateLine(line, true)} disabled={line.status === "generating"} className="rounded-md border px-2 py-1 text-xs hover:bg-muted disabled:opacity-50" title="随机 seed">
                    抽卡
                  </button>
                </div>
              </div>
            </div>
          </div>
        ))}
        {lines.length === 0 ? <div className="rounded-xl border border-dashed p-12 text-center text-muted-foreground">还没有提示词。填写上方文本并「保存提示词」。</div> : null}
      </section>

      <Dialog open={!!preview} onOpenChange={(open) => !open && setPreview(null)}>
        <DialogContent className="max-w-4xl border-0 bg-black/95 p-2">
          <DialogHeader className="sr-only">
            <DialogTitle>视频预览</DialogTitle>
            <DialogDescription>文生视频生成结果</DialogDescription>
          </DialogHeader>
          {preview ? <video src={preview} controls autoPlay className="max-h-[80vh] w-full rounded" /> : null}
        </DialogContent>
      </Dialog>
    </div>
  );
}
