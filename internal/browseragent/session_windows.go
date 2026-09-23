//go:build windows

package browseragent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrNoInteractiveSession 表示当前没有已登录的交互式桌面会话，无法显示浏览器窗口。
var ErrNoInteractiveSession = errors.New("当前没有已登录的交互式桌面会话")

// noActiveConsoleSessionID 是 WTSGetActiveConsoleSessionId 在无活动控制台会话时返回的哨兵值。
const noActiveConsoleSessionID = 0xFFFFFFFF

// interactiveDesktopName 是交互式窗口站桌面名称；窗口必须创建在该桌面上用户才能看到。
const interactiveDesktopName = "winsta0\\default"

// manualBrowserExecutableEnv 允许部署显式指定用于人工验证的系统浏览器可执行文件。
const manualBrowserExecutableEnv = "XIANYU_MANUAL_BROWSER"

// interactiveSessionLauncher 在当前用户会话中启动系统 Chrome/Edge 并开放回环调试端口。
// 服务运行在 Session 0 时通过 CreateProcessAsUser 切换到用户桌面；已在交互会话内运行时直接启动。
type interactiveSessionLauncher struct{}

// Launch 使用账号专用 profile 打开验证地址，等待进程创建并返回浏览器回环调试端口。
func (interactiveSessionLauncher) Launch(ctx context.Context, profileDir, verificationURL string) (int, error) {
	// err 是调用方 Context 的取消原因；启动浏览器前已取消则不再创建进程。
	if err := ctx.Err(); err != nil {
		return 0, ErrVerificationCancelled
	}
	// executable、lookupErr 是系统浏览器可执行文件及其查找失败原因。
	executable, lookupErr := findSystemBrowser()
	if lookupErr != nil {
		return 0, lookupErr
	}
	// err 是为该账号创建专用 profile 目录失败的错误；目录权限固定 0700，不得与其他账号共享。
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return 0, fmt.Errorf("创建人工验证浏览器目录失败: %w", err)
	}
	// port、portErr 是供 CDP 连接的回环调试端口。
	port, portErr := allocateLoopbackPort()
	if portErr != nil {
		return 0, portErr
	}
	// args 是人工验证浏览器启动参数；不含任何 Cookie、Token 或密码。
	args := []string{
		"--user-data-dir=" + profileDir,
		"--remote-debugging-port=" + strconv.Itoa(port),
		"--remote-allow-origins=http://127.0.0.1:" + strconv.Itoa(port),
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-features=Translate",
		"--new-window",
		verificationURL,
	}
	// err 是在用户会话中创建浏览器进程失败的错误，返回后调用方不得继续等待调试端口。
	if err := startBrowserInActiveSession(ctx, executable, args, profileDir); err != nil {
		return 0, err
	}
	return port, nil
}

// startBrowserInActiveSession 在活动控制台会话中启动浏览器进程。
// 进程句柄在启动后立即关闭，浏览器窗口保持运行直到用户自行关闭。
func startBrowserInActiveSession(ctx context.Context, executable string, args []string, workingDir string) error {
	// err 是调用方 Context 的取消原因；取消后不再创建任何用户会话进程。
	if err := ctx.Err(); err != nil {
		return ErrVerificationCancelled
	}
	// currentSessionID 是当前进程所在会话；与活动控制台会话一致时可以直接启动。
	currentSessionID, sessionErr := currentProcessSessionID()
	if sessionErr == nil && currentSessionID == windows.WTSGetActiveConsoleSessionId() {
		// command 是直接继承当前桌面时应启动的浏览器进程。
		command := exec.CommandContext(ctx, executable, args...)
		command.Dir = workingDir
		// err 是直接启动浏览器进程失败的错误，通常表示可执行文件不可用或用户环境拒绝执行。
		if err := command.Start(); err != nil {
			return fmt.Errorf("启动系统浏览器失败: %w", err)
		}
		// 释放子进程句柄，浏览器窗口由系统继续托管。
		_ = command.Process.Release()
		return nil
	}
	return startBrowserInConsoleSession(executable, args, workingDir)
}

// currentProcessSessionID 返回当前进程所属的终端服务会话标识。
func currentProcessSessionID() (uint32, error) {
	// sessionID 保存当前进程的会话标识。
	var sessionID uint32
	// err 是 ProcessIdToSessionId 调用失败的错误；失败时无法判定是否已在交互会话内。
	if err := windows.ProcessIdToSessionId(uint32(os.Getpid()), &sessionID); err != nil {
		return 0, err
	}
	return sessionID, nil
}

// startBrowserInConsoleSession 通过 CreateProcessAsUser 在活动控制台会话的用户桌面上启动浏览器。
func startBrowserInConsoleSession(executable string, args []string, workingDir string) error {
	// sessionID 是活动控制台会话标识；哨兵值表示当前没有已登录用户。
	sessionID := windows.WTSGetActiveConsoleSessionId()
	if sessionID == noActiveConsoleSessionID {
		return ErrNoInteractiveSession
	}
	// userToken 是活动会话用户的登录令牌。
	var userToken windows.Token
	// err 是查询活动会话用户令牌失败的错误；失败即视为没有可用的交互式桌面。
	if err := windows.WTSQueryUserToken(sessionID, &userToken); err != nil {
		return fmt.Errorf("%w: 查询用户令牌失败: %v", ErrNoInteractiveSession, err)
	}
	defer userToken.Close()
	// primaryToken 是 CreateProcessAsUser 需要的主令牌副本。
	var primaryToken windows.Token
	// err 是复制主令牌失败的错误，通常表示权限不足或会话已被注销。
	if err := windows.DuplicateTokenEx(userToken, windows.MAXIMUM_ALLOWED, nil, windows.SecurityImpersonation, windows.TokenPrimary, &primaryToken); err != nil {
		return fmt.Errorf("复制用户令牌失败: %w", err)
	}
	defer primaryToken.Close()
	// environment 是用户会话环境块，保证浏览器使用用户自己的配置目录。
	var environment *uint16
	// err 是构造用户环境块失败的错误，缺少环境块会导致浏览器读取到服务的变量而非用户变量。
	if err := windows.CreateEnvironmentBlock(&environment, primaryToken, false); err != nil {
		return fmt.Errorf("创建用户环境失败: %w", err)
	}
	defer func() { _ = windows.DestroyEnvironmentBlock(environment) }()
	// executablePtr、commandLinePtr 是 Windows API 需要的 UTF-16 参数。
	executablePtr, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return fmt.Errorf("浏览器可执行文件路径无效: %w", err)
	}
	// commandLinePtr、err 是 UTF-16 命令行及其构造失败原因；命令行由可执行文件与启动参数组合而成。
	commandLinePtr, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{executable}, args...)))
	if err != nil {
		return fmt.Errorf("浏览器命令行无效: %w", err)
	}
	// desktopPtr 指向交互式桌面，窗口创建在该桌面上用户才可见。
	desktopPtr, err := windows.UTF16PtrFromString(interactiveDesktopName)
	if err != nil {
		return fmt.Errorf("交互式桌面名称无效: %w", err)
	}
	// currentDirPtr 是浏览器进程起始目录；目录无效时退回系统默认。
	var currentDirPtr *uint16
	if strings.TrimSpace(workingDir) != "" {
		// workingDirPtr、dirErr 是起始目录的 UTF-16 形式及其转换失败原因；转换失败时保持 nil 使用系统默认目录。
		if workingDirPtr, dirErr := windows.UTF16PtrFromString(workingDir); dirErr == nil {
			currentDirPtr = workingDirPtr
		}
	}
	// startupInfo 描述新进程的桌面和结构体版本，Cb 必须与实际结构体大小一致。
	startupInfo := windows.StartupInfo{
		Cb:      uint32(unsafe.Sizeof(windows.StartupInfo{})),
		Desktop: desktopPtr,
	}
	// processInfo 保存新进程与主线程句柄；句柄在返回前关闭，浏览器继续运行。
	processInfo := windows.ProcessInformation{}
	// err 是 CreateProcessAsUser 调用失败的错误，涵盖令牌、桌面或命令行被拒绝的情形。
	if err := windows.CreateProcessAsUser(primaryToken, executablePtr, commandLinePtr, nil, nil, false,
		windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NEW_CONSOLE, environment, currentDirPtr, &startupInfo, &processInfo); err != nil {
		return fmt.Errorf("在用户会话中启动浏览器失败: %w", err)
	}
	_ = windows.CloseHandle(processInfo.Process)
	_ = windows.CloseHandle(processInfo.Thread)
	return nil
}

// allocateLoopbackPort 分配一个本机回环空闲端口供浏览器 CDP 使用。
func allocateLoopbackPort() (int, error) {
	// listener 绑定 :0 由系统分配空闲端口；关闭后端口立即归还。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("分配本机调试端口失败: %w", err)
	}
	defer func() { _ = listener.Close() }()
	// address 是监听地址，最后一个冒号后是实际分配到的端口。
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("无法解析本机调试端口")
	}
	return address.Port, nil
}

// findSystemBrowser 按显式配置、Chrome、Edge 顺序查找系统浏览器可执行文件。
// 查找失败时返回明确错误，调用方不得静默回退到自动化浏览器。
func findSystemBrowser() (string, error) {
	// configured 是部署显式指定的浏览器路径。
	if configured := strings.TrimSpace(os.Getenv(manualBrowserExecutableEnv)); configured != "" {
		// err 是探测显式配置路径失败的错误（不存在或无权限），此时必须报错而不是回退到其他浏览器。
		if _, err := os.Stat(configured); err != nil {
			return "", fmt.Errorf("配置的系统浏览器不可用: %w", err)
		}
		return configured, nil
	}
	// candidates 是按优先级排列的候选路径；包含 PATH 名称和常见安装位置。
	candidates := []string{
		filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
		"chrome.exe",
		"msedge.exe",
	}
	// candidate 是当前待检查的浏览器路径或命令名。
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		// resolved、err 是候选路径解析后的绝对路径及其查找失败原因；err 为 nil 表示已找到可用浏览器。
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved, nil
		}
	}
	return "", errors.New("未找到 Google Chrome 或 Microsoft Edge，请安装其中一个或设置 XIANYU_MANUAL_BROWSER")
}
