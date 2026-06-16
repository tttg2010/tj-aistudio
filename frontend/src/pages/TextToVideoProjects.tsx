import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import axios from "axios";
import { Film, Pencil, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

import type { TextToVideoProject } from "@/types";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";

export default function TextToVideoProjects() {
  const navigate = useNavigate();
  const [projects, setProjects] = useState<TextToVideoProject[]>([]);
  const [loading, setLoading] = useState(false);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<TextToVideoProject | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<TextToVideoProject | null>(null);
  const [name, setName] = useState("");
  const [code, setCode] = useState("");
  const [description, setDescription] = useState("");
  const [text, setText] = useState("");
  const [saving, setSaving] = useState(false);

  const fetchProjects = async () => {
    setLoading(true);
    try {
      const res = await axios.get("/api/text-to-video-projects");
      setProjects(Array.isArray(res.data) ? res.data : []);
    } catch (err) {
      console.error(err);
      toast.error("获取文生视频项目失败");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void fetchProjects();
  }, []);

  const resetForm = () => {
    setName("");
    setCode("");
    setDescription("");
    setText("");
  };

  const openCreate = () => {
    resetForm();
    setEditing(null);
    setDialogOpen(true);
  };

  const openEdit = (p: TextToVideoProject) => {
    setEditing(p);
    setName(p.name || "");
    setCode(p.code || "");
    setDescription(p.description || "");
    setText(p.text || "");
    setDialogOpen(true);
  };

  const saveProject = async () => {
    if (!name.trim() || !code.trim()) {
      toast.error("请填写项目名称和项目文件名");
      return;
    }
    setSaving(true);
    try {
      const payload = { name: name.trim(), code: code.trim(), description: description.trim(), text: text.trim() };
      const res = editing
        ? await axios.put(`/api/text-to-video-projects/${editing.id}`, payload)
        : await axios.post("/api/text-to-video-projects", payload);
      toast.success(editing ? "项目已更新" : "项目已创建");
      setDialogOpen(false);
      resetForm();
      setEditing(null);
      await fetchProjects();
      if (!editing) navigate(`/text-to-video/${res.data.id}`);
    } catch (err: any) {
      console.error(err);
      toast.error(err.response?.data?.error || "保存失败");
    } finally {
      setSaving(false);
    }
  };

  const deleteProject = async () => {
    if (!deleteTarget) return;
    try {
      await axios.delete(`/api/text-to-video-projects/${deleteTarget.id}`);
      toast.success("项目已删除");
      setDeleteTarget(null);
      await fetchProjects();
    } catch (err: any) {
      console.error(err);
      toast.error(err.response?.data?.error || "删除失败");
    }
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-3xl font-bold">文生视频</h1>
          <p className="mt-1 text-sm text-muted-foreground">输入提示词，用 RunningHub 托管的 LTX2.3 文生视频工作流直接出片（每段提示词一个视频）。</p>
        </div>
        <button onClick={openCreate} className="flex items-center gap-2 rounded-md bg-primary px-4 py-2 text-primary-foreground hover:bg-primary/90">
          <Plus className="h-4 w-4" />
          新建项目
        </button>
      </div>

      <div className="space-y-3">
        {projects.map((project) => (
          <div key={project.id} className="rounded-xl border bg-card p-4 shadow-sm">
            <div className="flex items-center gap-4">
              <button onClick={() => navigate(`/text-to-video/${project.id}`)} className="flex h-16 w-16 shrink-0 items-center justify-center rounded-xl border bg-muted/40">
                <Film className="h-8 w-8 text-muted-foreground" />
              </button>
              <button onClick={() => navigate(`/text-to-video/${project.id}`)} className="min-w-0 flex-1 text-left">
                <h3 className="truncate text-lg font-semibold">{project.name}</h3>
                <p className="text-xs text-muted-foreground">文件名：{project.code}</p>
                {project.description ? <p className="mt-1 line-clamp-2 text-sm text-muted-foreground">{project.description}</p> : null}
              </button>
              <div className="flex gap-2">
                <button onClick={() => openEdit(project)} className="rounded-md border px-3 py-2 text-sm hover:bg-muted">
                  <Pencil className="mr-1 inline h-4 w-4" />
                  编辑
                </button>
                <button onClick={() => setDeleteTarget(project)} className="rounded-md border px-3 py-2 text-sm text-destructive hover:bg-destructive/10">
                  <Trash2 className="mr-1 inline h-4 w-4" />
                  删除
                </button>
              </div>
            </div>
          </div>
        ))}
        {!loading && projects.length === 0 ? <div className="rounded-xl border border-dashed p-12 text-center text-muted-foreground">还没有文生视频项目。</div> : null}
      </div>

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>{editing ? "编辑文生视频项目" : "新建文生视频项目"}</DialogTitle>
            <DialogDescription>每段提示词生成一段视频。多段之间用空行分隔。</DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div>
              <label className="text-sm font-medium">项目名称</label>
              <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="例如：厨房短剧片段" />
            </div>
            <div>
              <label className="text-sm font-medium">项目文件名</label>
              <Input value={code} onChange={(e) => setCode(e.target.value)} placeholder="例如：t2v_kitchen_01" disabled={!!editing} />
            </div>
            <div>
              <label className="text-sm font-medium">备注</label>
              <Input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="只给自己看，不参与生成" />
            </div>
            <div>
              <label className="text-sm font-medium">提示词（可选，每段一个视频，空行分隔）</label>
              <Textarea rows={6} value={text} onChange={(e) => setText(e.target.value)} placeholder={"9:16 镜头缓慢推进，厨房，自然光...\n\n下一段提示词..."} />
            </div>
          </div>
          <DialogFooter>
            <button onClick={() => setDialogOpen(false)} className="rounded-md border px-4 py-2 text-sm">取消</button>
            <button onClick={saveProject} disabled={saving} className="rounded-md bg-primary px-4 py-2 text-sm text-primary-foreground disabled:opacity-60">
              {saving ? "保存中..." : "保存"}
            </button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <AlertDialog open={!!deleteTarget} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除文生视频项目？</AlertDialogTitle>
            <AlertDialogDescription>会删除项目、生成的视频和所有物理文件。</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction onClick={deleteProject} className="bg-destructive text-destructive-foreground hover:bg-destructive/90">删除</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
