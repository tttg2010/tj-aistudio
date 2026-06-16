package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// RunningHub task concurrency gate. The free tier allows a single concurrent
// task, so all RunningHub generation (image/video/audio) is serialized through
// this gate; the limit is read live from settings (runninghub_concurrency, >=1).
var (
	rhGateMu     sync.Mutex
	rhGateActive int
	rhGateCond   = sync.NewCond(&rhGateMu)
)

func rhGateAcquire() {
	rhGateMu.Lock()
	for rhGateActive >= getRunningHubConcurrency() {
		rhGateCond.Wait()
	}
	rhGateActive++
	rhGateMu.Unlock()
}

func rhGateRelease() {
	rhGateMu.Lock()
	if rhGateActive > 0 {
		rhGateActive--
	}
	rhGateCond.Broadcast()
	rhGateMu.Unlock()
}

// RunningHub (https://www.runninghub.cn) classic OpenAPI client.
//
// RunningHub cannot run a raw local workflow JSON. A workflow must first be
// published on the platform to obtain a workflowId; a task is then created by
// referencing that workflowId and overriding node inputs via nodeInfoList.
// Go-Ai-Studio already injects parameters into the ComfyUI API-format graph, so
// we derive nodeInfoList by diffing the injected graph against the pristine
// template (see BuildRunningHubNodeInfoList).

// RHNodeInfo is one node-input override sent to RunningHub. fieldValue is always
// a string per the RunningHub API.
type RHNodeInfo struct {
	NodeID     string `json:"nodeId"`
	FieldName  string `json:"fieldName"`
	FieldValue string `json:"fieldValue"`
}

// RHOutput is one generated result file returned by RunningHub.
type RHOutput struct {
	FileURL  string `json:"fileUrl"`
	FileType string `json:"fileType"`
}

// runningHubEnvelope is the common {code, msg, data} response wrapper.
type runningHubEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// RunningHub task status strings.
const (
	runningHubStatusSuccess = "SUCCESS"
	runningHubStatusFailed  = "FAILED"
)

func runningHubHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// runningHubPostJSON posts a JSON body to {base}{path}, injects apiKey, and
// returns the decoded envelope (validated for transport + business code).
func runningHubPostJSON(path string, body map[string]interface{}, timeout time.Duration) (*runningHubEnvelope, error) {
	cfg := getRunningHubConfig()
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("RunningHub API Key 未配置")
	}
	if body == nil {
		body = map[string]interface{}{}
	}
	body["apiKey"] = cfg.APIKey

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal RunningHub request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, cfg.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Host", "www.runninghub.cn")

	resp, err := runningHubHTTPClient(timeout).Do(req)
	if err != nil {
		return nil, fmt.Errorf("RunningHub 请求失败: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("RunningHub 返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var env runningHubEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("failed to decode RunningHub response: %w (body=%s)", err, strings.TrimSpace(string(raw)))
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("RunningHub 接口错误 code=%d msg=%s", env.Code, strings.TrimSpace(env.Msg))
	}
	return &env, nil
}

// runningHubCreateTask creates a task for a published workflow and returns its taskId.
func runningHubCreateTask(workflowID string, nodeInfoList []RHNodeInfo) (string, error) {
	if strings.TrimSpace(workflowID) == "" {
		return "", fmt.Errorf("workflowId 为空（该 workflow 未在设置里映射 RunningHub ID）")
	}
	cfg := getRunningHubConfig()

	body := map[string]interface{}{
		"workflowId": strings.TrimSpace(workflowID),
	}
	if nodeInfoList == nil {
		nodeInfoList = []RHNodeInfo{}
	}
	nodeInfoList = normalizeRunningHubSeeds(nodeInfoList)
	body["nodeInfoList"] = nodeInfoList
	if cfg.InstanceType != "" {
		body["instanceType"] = cfg.InstanceType
	}

	env, err := runningHubPostJSON("/task/openapi/create", body, 30*time.Second)
	if err != nil {
		return "", err
	}

	var data struct {
		TaskID     string `json:"taskId"`
		TaskStatus string `json:"taskStatus"`
		PromptTips string `json:"promptTips"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return "", fmt.Errorf("无法解析 RunningHub create 响应: %w", err)
	}
	if strings.TrimSpace(data.TaskID) == "" {
		return "", fmt.Errorf("RunningHub 未返回 taskId（promptTips=%s）", strings.TrimSpace(data.PromptTips))
	}
	return strings.TrimSpace(data.TaskID), nil
}

// normalizeRunningHubSeeds replaces negative seed values (ComfyUI's "-1 = random"
// convention, which RunningHub's KSampler rejects with min=0) with a concrete
// non-negative random seed, preserving the randomize-each-run intent.
func normalizeRunningHubSeeds(list []RHNodeInfo) []RHNodeInfo {
	for i := range list {
		fn := strings.ToLower(strings.TrimSpace(list[i].FieldName))
		if fn != "seed" && fn != "noise_seed" && fn != "rand_seed" && !strings.HasSuffix(fn, "_seed") {
			continue
		}
		if v, err := strconv.ParseInt(strings.TrimSpace(list[i].FieldValue), 10, 64); err == nil && v < 0 {
			list[i].FieldValue = strconv.FormatInt(rand.Int63(), 10)
		}
	}
	return list
}

// runningHubFetchFailedReason best-effort fetches a failed task's reason via the
// outputs endpoint (which returns data.failedReason for failed tasks).
func runningHubFetchFailedReason(taskID string) string {
	cfg := getRunningHubConfig()
	payload, _ := json.Marshal(map[string]string{"apiKey": cfg.APIKey, "taskId": taskID})
	req, err := http.NewRequest(http.MethodPost, cfg.BaseURL+"/task/openapi/outputs", bytes.NewReader(payload))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Host", "www.runninghub.cn")
	resp, err := runningHubHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env struct {
		Data struct {
			// failedReason is sometimes a bare string ("433||{...}") and sometimes
			// an object ({exception_message, node_id, ...}); accept both.
			FailedReason json.RawMessage `json:"failedReason"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil || len(env.Data.FailedReason) == 0 {
		return ""
	}
	fr := env.Data.FailedReason

	// String form.
	var s string
	if json.Unmarshal(fr, &s) == nil {
		return strings.TrimSpace(s)
	}
	// Object form — pull out the human-useful fields into one line.
	var obj struct {
		ExceptionType    string `json:"exception_type"`
		ExceptionMessage string `json:"exception_message"`
		NodeID           string `json:"node_id"`
		NodeName         string `json:"node_name"`
		Traceback        string `json:"traceback"`
	}
	if json.Unmarshal(fr, &obj) == nil {
		parts := make([]string, 0, 4)
		if strings.TrimSpace(obj.NodeName) != "" || strings.TrimSpace(obj.NodeID) != "" {
			parts = append(parts, fmt.Sprintf("节点 %s(%s)", strings.TrimSpace(obj.NodeName), strings.TrimSpace(obj.NodeID)))
		}
		if strings.TrimSpace(obj.ExceptionMessage) != "" {
			parts = append(parts, strings.TrimSpace(obj.ExceptionMessage))
		}
		if strings.TrimSpace(obj.Traceback) != "" {
			parts = append(parts, strings.TrimSpace(obj.Traceback))
		}
		if len(parts) > 0 {
			return strings.Join(parts, " | ")
		}
	}
	// Fallback: raw JSON.
	return strings.TrimSpace(string(fr))
}

// runningHubQueryStatus returns the task status string (QUEUED/RUNNING/SUCCESS/FAILED).
// The status endpoint may return data as a bare string or as an object with a
// status/taskStatus field; both are handled.
func runningHubQueryStatus(taskID string) (string, error) {
	env, err := runningHubPostJSON("/task/openapi/status", map[string]interface{}{"taskId": taskID}, 15*time.Second)
	if err != nil {
		return "", err
	}

	// Try bare string first.
	var statusStr string
	if err := json.Unmarshal(env.Data, &statusStr); err == nil && strings.TrimSpace(statusStr) != "" {
		return strings.ToUpper(strings.TrimSpace(statusStr)), nil
	}
	// Fall back to an object with a status field.
	var obj struct {
		Status     string `json:"status"`
		TaskStatus string `json:"taskStatus"`
	}
	if err := json.Unmarshal(env.Data, &obj); err == nil {
		if s := strings.TrimSpace(obj.TaskStatus); s != "" {
			return strings.ToUpper(s), nil
		}
		if s := strings.TrimSpace(obj.Status); s != "" {
			return strings.ToUpper(s), nil
		}
	}
	return "", fmt.Errorf("无法解析 RunningHub status 响应: %s", strings.TrimSpace(string(env.Data)))
}

// runningHubFetchOutputs returns the result files for a completed task.
func runningHubFetchOutputs(taskID string) ([]RHOutput, error) {
	env, err := runningHubPostJSON("/task/openapi/outputs", map[string]interface{}{"taskId": taskID}, 30*time.Second)
	if err != nil {
		return nil, err
	}
	var outputs []RHOutput
	if err := json.Unmarshal(env.Data, &outputs); err != nil {
		return nil, fmt.Errorf("无法解析 RunningHub outputs 响应: %w (body=%s)", err, strings.TrimSpace(string(env.Data)))
	}
	return outputs, nil
}

// runningHubUploadImage uploads a local image to RunningHub for a LoadImage node.
func runningHubUploadImage(localPath string) (string, error) {
	return runningHubUploadFile(localPath, "image")
}

// runningHubUploadAudio uploads a local audio file to RunningHub for a
// LoadAudio / reference-audio node.
func runningHubUploadAudio(localPath string) (string, error) {
	return runningHubUploadFile(localPath, "audio")
}

// runningHubUploadFile uploads a local file (fileType: image/audio/video) to
// RunningHub and returns the platform file name to inject into a loader node.
// Mirrors UploadToComfyUIInput.
func runningHubUploadFile(localPath string, fileType string) (string, error) {
	cfg := getRunningHubConfig()
	if cfg.APIKey == "" {
		return "", fmt.Errorf("RunningHub API Key 未配置")
	}

	file, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("failed to open local file: %w", err)
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	_ = writer.WriteField("apiKey", cfg.APIKey)
	_ = writer.WriteField("fileType", fileType)
	part, err := writer.CreateFormFile("file", filepath.Base(localPath))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", err
	}
	writer.Close()
	bodyBytes := body.Bytes()
	contentType := writer.FormDataContentType()

	// Upload over a possibly-flaky link to RunningHub; retry transient network
	// errors (EOF / connection reset) and 5xx with linear backoff.
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequest(http.MethodPost, cfg.BaseURL+"/task/openapi/upload", bytes.NewReader(bodyBytes))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Host", "www.runninghub.cn")

		resp, err := runningHubHTTPClient(120 * time.Second).Do(req)
		if err != nil {
			lastErr = fmt.Errorf("RunningHub 上传失败: %w", err)
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("RunningHub 上传返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
			if resp.StatusCode >= 500 {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return "", lastErr
		}

		var env runningHubEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return "", fmt.Errorf("无法解析 RunningHub 上传响应: %w (body=%s)", err, strings.TrimSpace(string(raw)))
		}
		if env.Code != 0 {
			return "", fmt.Errorf("RunningHub 上传错误 code=%d msg=%s", env.Code, strings.TrimSpace(env.Msg))
		}
		var data struct {
			FileName string `json:"fileName"`
		}
		if err := json.Unmarshal(env.Data, &data); err != nil || strings.TrimSpace(data.FileName) == "" {
			return "", fmt.Errorf("RunningHub 上传未返回 fileName: %s", strings.TrimSpace(string(env.Data)))
		}
		return strings.TrimSpace(data.FileName), nil
	}
	return "", fmt.Errorf("RunningHub 上传失败（已重试 3 次）: %w", lastErr)
}

// uploadReferenceImageForProvider uploads a local reference image to the active
// image-generation backend and returns the file name to inject into a LoadImage
// node (RunningHub platform file name, or the local ComfyUI input name).
func uploadReferenceImageForProvider(provider string, localPath string) (string, error) {
	if provider == ImageGenerationProviderRunningHub {
		return runningHubUploadImage(localPath)
	}
	return UploadToComfyUIInput(localPath)
}

// BuildRunningHubNodeInfoList derives the nodeInfoList by diffing an injected
// ComfyUI API-format graph against its pristine on-disk template: every scalar
// node input that changed becomes one override entry. Reference-type inputs
// (arrays like ["6", 0] linking nodes) are skipped.
func BuildRunningHubNodeInfoList(templateJSON, injectedJSON map[string]interface{}) []RHNodeInfo {
	result := make([]RHNodeInfo, 0)
	for nodeID, injectedNodeRaw := range injectedJSON {
		injectedNode, ok := injectedNodeRaw.(map[string]interface{})
		if !ok {
			continue
		}
		injectedInputs, ok := injectedNode["inputs"].(map[string]interface{})
		if !ok {
			continue
		}

		var templateInputs map[string]interface{}
		if templateNode, ok := templateJSON[nodeID].(map[string]interface{}); ok {
			templateInputs, _ = templateNode["inputs"].(map[string]interface{})
		}

		for fieldName, injectedValue := range injectedInputs {
			injStr, ok := scalarToFieldValue(injectedValue)
			if !ok {
				// Reference link or nested object — not an overridable scalar.
				continue
			}
			var tmplStr string
			tmplOK := false
			if templateInputs != nil {
				tmplStr, tmplOK = scalarToFieldValue(templateInputs[fieldName])
			}
			if tmplOK && tmplStr == injStr {
				continue // unchanged from template
			}
			result = append(result, RHNodeInfo{
				NodeID:     nodeID,
				FieldName:  fieldName,
				FieldValue: injStr,
			})
		}
	}
	return result
}

// scalarToFieldValue converts a JSON scalar (string/number/bool) to the string
// form RunningHub expects. Returns ok=false for arrays/objects/nil.
func scalarToFieldValue(v interface{}) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case bool:
		return strconv.FormatBool(t), true
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10), true
		}
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case json.Number:
		return t.String(), true
	case int:
		return strconv.Itoa(t), true
	case int64:
		return strconv.FormatInt(t, 10), true
	default:
		return "", false
	}
}

// downloadRemoteAsset downloads a remote URL to savePath, creating parent dirs.
func downloadRemoteAsset(sourceURL string, savePath string) error {
	if strings.TrimSpace(sourceURL) == "" {
		return fmt.Errorf("download url is empty")
	}
	if err := os.MkdirAll(filepath.Dir(savePath), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	req, err := http.NewRequest(http.MethodGet, sourceURL, nil)
	if err != nil {
		return err
	}
	resp, err := runningHubHTTPClient(30 * time.Minute).Do(req)
	if err != nil {
		return fmt.Errorf("failed to download asset: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("asset download failed: %s", resp.Status)
	}

	out, err := os.Create(savePath)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return err
	}
	return nil
}

// runningHubWaitForOutputs polls a task to completion and returns its outputs.
// pollInterval/timeout govern the loop; SUCCESS returns outputs, FAILED errors.
func runningHubWaitForOutputs(taskID string, pollInterval time.Duration, timeout time.Duration) ([]RHOutput, error) {
	deadline := time.Now().Add(timeout)
	for {
		status, err := runningHubQueryStatus(taskID)
		if err != nil {
			// Transient query error: keep trying until the deadline.
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("RunningHub 轮询超时: %w", err)
			}
			time.Sleep(pollInterval)
			continue
		}
		switch status {
		case runningHubStatusSuccess:
			return runningHubFetchOutputs(taskID)
		case runningHubStatusFailed:
			if reason := runningHubFetchFailedReason(taskID); reason != "" {
				Log(LogLevelError, "RunningHub 任务失败", fmt.Sprintf("taskId=%s\n原因: %s", taskID, reason))
				return nil, fmt.Errorf("RunningHub 任务失败 (task=%s): %s", taskID, reason)
			}
			Log(LogLevelError, "RunningHub 任务失败", fmt.Sprintf("taskId=%s（RunningHub 未返回失败详情）", taskID))
			return nil, fmt.Errorf("RunningHub 任务失败 (task=%s)", taskID)
		default:
			// QUEUED / RUNNING / other transient states.
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("RunningHub 任务超时 (task=%s, last_status=%s)", taskID, status)
			}
			time.Sleep(pollInterval)
		}
	}
}

// runRunningHubImageTask runs the published workflow mapped to templateFileName
// on RunningHub. nodeInfoList is derived by diffing the injected graph against
// its template; the first image output is downloaded into saveDir and its local
// web path ("/output/...") is returned.
func runRunningHubImageTask(templateFileName string, template, injected map[string]interface{}, saveDir, fileBase string) (string, error) {
	workflowID := lookupRunningHubWorkflowID(templateFileName)
	if workflowID == "" {
		return "", fmt.Errorf("workflow「%s」未在设置里映射 RunningHub workflowId", templateFileName)
	}
	nodeInfoList := BuildRunningHubNodeInfoList(template, injected)

	rhGateAcquire()
	defer rhGateRelease()

	taskID, err := runningHubCreateTask(workflowID, nodeInfoList)
	if err != nil {
		return "", err
	}
	Log(LogLevelInfo, "RunningHub 任务已创建", fmt.Sprintf("workflowId=%s taskId=%s 覆盖节点数=%d", workflowID, taskID, len(nodeInfoList)))

	outputs, err := runningHubWaitForOutputs(taskID, 3*time.Second, 10*time.Minute)
	if err != nil {
		return "", err
	}
	output := pickRunningHubImageOutput(outputs)
	if strings.TrimSpace(output.FileURL) == "" {
		return "", fmt.Errorf("RunningHub 任务完成但未返回图片输出")
	}

	if err := os.MkdirAll(saveDir, 0755); err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(output.FileURL))
	if ext == "" || len(ext) > 5 {
		if output.FileType != "" {
			ext = "." + strings.TrimPrefix(strings.ToLower(output.FileType), ".")
		} else {
			ext = ".png"
		}
	}
	savePath := filepath.Join(saveDir, fmt.Sprintf("%s_%d%s", fileBase, time.Now().UnixNano(), ext))
	if err := downloadRemoteAsset(output.FileURL, savePath); err != nil {
		return "", err
	}
	return "/" + filepath.ToSlash(savePath), nil
}

// runRunningHubVideoTask runs the published video workflow mapped to
// templateFileName on RunningHub and downloads the first video output into
// saveDir, returning its local web path ("/output/...").
func runRunningHubVideoTask(templateFileName string, template, injected map[string]interface{}, saveDir, fileBase string) (string, error) {
	workflowID := lookupRunningHubWorkflowID(templateFileName)
	if workflowID == "" {
		return "", fmt.Errorf("workflow「%s」未在设置里映射 RunningHub workflowId", templateFileName)
	}
	nodeInfoList := BuildRunningHubNodeInfoList(template, injected)

	rhGateAcquire()
	defer rhGateRelease()

	taskID, err := runningHubCreateTask(workflowID, nodeInfoList)
	if err != nil {
		return "", err
	}
	Log(LogLevelInfo, "RunningHub 视频任务已创建", fmt.Sprintf("workflowId=%s taskId=%s 覆盖节点数=%d", workflowID, taskID, len(nodeInfoList)))

	// Video generation is slower than images; allow a longer ceiling.
	outputs, err := runningHubWaitForOutputs(taskID, 5*time.Second, 20*time.Minute)
	if err != nil {
		return "", err
	}
	output := pickRunningHubVideoOutput(outputs)
	if strings.TrimSpace(output.FileURL) == "" {
		return "", fmt.Errorf("RunningHub 任务完成但未返回视频输出")
	}

	if err := os.MkdirAll(saveDir, 0755); err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(output.FileURL))
	if ext == "" || len(ext) > 5 {
		if output.FileType != "" {
			ext = "." + strings.TrimPrefix(strings.ToLower(output.FileType), ".")
		} else {
			ext = ".mp4"
		}
	}
	savePath := filepath.Join(saveDir, fmt.Sprintf("%s_%d%s", fileBase, time.Now().UnixNano(), ext))
	if err := downloadRemoteAsset(output.FileURL, savePath); err != nil {
		return "", err
	}
	return "/" + filepath.ToSlash(savePath), nil
}

// runRunningHubAudioTask runs the published audio/TTS workflow mapped to
// templateFileName on RunningHub and downloads the first audio output into
// saveDir, returning its local web path ("/output/...").
func runRunningHubAudioTask(templateFileName string, template, injected map[string]interface{}, saveDir, fileBase string) (string, error) {
	workflowID := lookupRunningHubWorkflowID(templateFileName)
	if workflowID == "" {
		return "", fmt.Errorf("workflow「%s」未在设置里映射 RunningHub workflowId", templateFileName)
	}
	nodeInfoList := BuildRunningHubNodeInfoList(template, injected)

	rhGateAcquire()
	defer rhGateRelease()

	taskID, err := runningHubCreateTask(workflowID, nodeInfoList)
	if err != nil {
		return "", err
	}
	Log(LogLevelInfo, "RunningHub 音频任务已创建", fmt.Sprintf("workflowId=%s taskId=%s 覆盖节点数=%d", workflowID, taskID, len(nodeInfoList)))

	outputs, err := runningHubWaitForOutputs(taskID, 3*time.Second, 10*time.Minute)
	if err != nil {
		return "", err
	}
	output := pickRunningHubAudioOutput(outputs)
	if strings.TrimSpace(output.FileURL) == "" {
		return "", fmt.Errorf("RunningHub 任务完成但未返回音频输出")
	}

	if err := os.MkdirAll(saveDir, 0755); err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(output.FileURL))
	if ext == "" || len(ext) > 5 {
		if output.FileType != "" {
			ext = "." + strings.TrimPrefix(strings.ToLower(output.FileType), ".")
		} else {
			ext = ".mp3"
		}
	}
	savePath := filepath.Join(saveDir, fmt.Sprintf("%s_%d%s", fileBase, time.Now().UnixNano(), ext))
	if err := downloadRemoteAsset(output.FileURL, savePath); err != nil {
		return "", err
	}
	return "/" + filepath.ToSlash(savePath), nil
}

// pickRunningHubAudioOutput returns the first audio-like output (by fileType or
// URL extension), falling back to the first output of any kind.
func pickRunningHubAudioOutput(outputs []RHOutput) RHOutput {
	for _, o := range outputs {
		switch strings.ToLower(strings.TrimPrefix(o.FileType, ".")) {
		case "mp3", "wav", "flac", "aac", "ogg", "m4a", "opus":
			return o
		}
		u := strings.ToLower(o.FileURL)
		if strings.HasSuffix(u, ".mp3") || strings.HasSuffix(u, ".wav") ||
			strings.HasSuffix(u, ".flac") || strings.HasSuffix(u, ".m4a") ||
			strings.HasSuffix(u, ".ogg") {
			return o
		}
	}
	if len(outputs) > 0 {
		return outputs[0]
	}
	return RHOutput{}
}

// pickRunningHubVideoOutput returns the first video-like output (by fileType or
// URL extension), falling back to the first output of any kind.
func pickRunningHubVideoOutput(outputs []RHOutput) RHOutput {
	for _, o := range outputs {
		switch strings.ToLower(strings.TrimPrefix(o.FileType, ".")) {
		case "mp4", "webm", "mov", "mkv", "gif", "avi":
			return o
		}
		u := strings.ToLower(o.FileURL)
		if strings.HasSuffix(u, ".mp4") || strings.HasSuffix(u, ".webm") ||
			strings.HasSuffix(u, ".mov") || strings.HasSuffix(u, ".gif") {
			return o
		}
	}
	if len(outputs) > 0 {
		return outputs[0]
	}
	return RHOutput{}
}

// pickRunningHubImageOutput returns the first image-like output (by fileType or
// URL extension), falling back to the first output of any kind.
func pickRunningHubImageOutput(outputs []RHOutput) RHOutput {
	for _, o := range outputs {
		switch strings.ToLower(strings.TrimPrefix(o.FileType, ".")) {
		case "png", "jpg", "jpeg", "webp", "bmp", "gif":
			return o
		}
		u := strings.ToLower(o.FileURL)
		if strings.HasSuffix(u, ".png") || strings.HasSuffix(u, ".jpg") ||
			strings.HasSuffix(u, ".jpeg") || strings.HasSuffix(u, ".webp") {
			return o
		}
	}
	if len(outputs) > 0 {
		return outputs[0]
	}
	return RHOutput{}
}

// GetRunningHubStatus checks RunningHub connectivity and API key validity by
// querying the account status endpoint. Mirrors GetComfyUIStatus.
func GetRunningHubStatus(c *gin.Context) {
	cfg := getRunningHubConfig()
	if cfg.APIKey == "" {
		c.JSON(http.StatusOK, gin.H{
			"status":  "unconfigured",
			"address": cfg.BaseURL,
			"error":   "RunningHub API Key 未配置",
		})
		return
	}

	env, err := runningHubPostJSON("/uc/openapi/accountStatus", map[string]interface{}{}, 5*time.Second)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"status":  "offline",
			"address": cfg.BaseURL,
			"error":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "online",
		"address": cfg.BaseURL,
		"data":    json.RawMessage(env.Data),
	})
}
