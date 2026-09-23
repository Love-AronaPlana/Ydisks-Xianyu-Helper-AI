// @vitest-environment jsdom
import { renderHook, waitFor, act } from '@testing-library/react';
import { beforeEach, describe, expect, test, vi } from 'vitest';
import type { ChatHumanHandoff, ChatSession } from './models';

/** getHandoffMock、setHandoffMock、clearHandoffMock 保存人工接管 API 替身。 */
const { getHandoffMock, setHandoffMock, clearHandoffMock } = vi.hoisted(/* apiMockFactory 创建人工接管 API 替身。 */ () => ({
  getHandoffMock: vi.fn(),
  setHandoffMock: vi.fn(),
  clearHandoffMock: vi.fn(),
}));

vi.mock(/* 当前回调提供人工接管 Hook 所需的 API 依赖。 */ './api', /* 模块替身工厂返回接管所需的三个 API 替身。 */ () => ({
  getChatHumanHandoff: getHandoffMock,
  setChatHumanHandoff: setHandoffMock,
  clearChatHumanHandoff: clearHandoffMock,
}));

import { formatHandoffRemaining, useChatHumanHandoff } from './handoff';

/** handoffSession 返回一个用于接管测试的卖家侧会话。 */
const handoffSession = (/* chatID 是会话标识。 */ chatID: string): ChatSession => ({ account_id: 'acc1', chat_id: chatID } as ChatSession);

/** handoffActive 构造一个处于接管中的服务端状态。 */
const handoffActive = (/* remainingSeconds 是服务端返回的剩余接管秒数。 */ remainingSeconds: number): ChatHumanHandoff => ({ account_id: 'acc1', buyer_id: 'buyer1', paused_until: 1_700_000_000 + remainingSeconds, active: true, remaining_seconds: remainingSeconds });

describe(/* 当前回调覆盖人工接管 Hook 的读取、设置、结束与隔离。 */ 'useChatHumanHandoff', () => {
  beforeEach(/* 当前回调重置人工接管 API 替身并恢复真实计时器。 */ () => {
    vi.clearAllMocks();
    vi.useRealTimers();
    getHandoffMock.mockResolvedValue({ account_id: 'acc1', buyer_id: 'buyer1', paused_until: 0, active: false, remaining_seconds: 0 });
    setHandoffMock.mockResolvedValue(handoffActive(300));
    clearHandoffMock.mockResolvedValue({ account_id: 'acc1', buyer_id: 'buyer1', paused_until: 0, active: false, remaining_seconds: 0 });
  });

  test(/* 当前回调验证打开会话时读取接管状态并显示倒计时。 */ '读取会话接管状态并展示剩余时间', /* 当前回调执行一次接管状态读取。 */ async () => {
    // view 是被测 Hook 的渲染结果。
    const view = renderHook(/* 当前回调为选定会话创建接管 Hook。 */ () => useChatHumanHandoff('acc1', handoffSession('chat1')));
    await waitFor(/* 当前回调等待接管状态读取完成。 */ () => expect(view.result.current.loading).toBe(false));
    expect(getHandoffMock).toHaveBeenCalledWith('acc1', 'chat1', expect.objectContaining({ signal: expect.any(AbortSignal) }));
    expect(view.result.current.handoff?.active).toBe(false);
  });

  test(/* 当前回调验证按分钟接管后本地倒计时按服务端剩余时间初始化。 */ '按分钟接管后记录剩余时间', /* 当前回调执行一次按分钟接管。 */ async () => {
    // view 是被测 Hook 的渲染结果，用于断言本地倒计时。
    const view = renderHook(/* 当前回调为选定会话创建接管 Hook。 */ () => useChatHumanHandoff('acc1', handoffSession('chat1')));
    await waitFor(/* 当前回调等待初次读取完成。 */ () => expect(view.result.current.loading).toBe(false));
    await act(/* 当前回调触发一次 5 分钟接管。 */ async () => {
      await view.result.current.start(5);
    });
    expect(setHandoffMock).toHaveBeenCalledWith('acc1', 'chat1', 5, expect.objectContaining({ signal: expect.any(AbortSignal) }));
    expect(view.result.current.handoff?.active).toBe(true);
    expect(view.result.current.remainingSeconds).toBe(300);
  });

  test(/* 当前回调验证提前结束接管后本地状态立即变为未接管。 */ '提前结束接管后清除本地状态', /* 当前回调验证提前结束接管。 */ async () => {
    getHandoffMock.mockResolvedValue(handoffActive(600));
    // view 是处于接管中的 Hook 渲染结果。
    const view = renderHook(/* 当前回调为选定会话创建接管 Hook。 */ () => useChatHumanHandoff('acc1', handoffSession('chat1')));
    await waitFor(/* 当前回调等待接管状态读取完成。 */ () => expect(view.result.current.remainingSeconds).toBe(600));
    await act(/* 当前回调触发提前结束接管。 */ async () => {
      await view.result.current.stop();
    });
    expect(clearHandoffMock).toHaveBeenCalledWith('acc1', 'chat1', expect.objectContaining({ signal: expect.any(AbortSignal) }));
    expect(view.result.current.handoff?.active).toBe(false);
    expect(view.result.current.remainingSeconds).toBe(0);
  });

  test(/* 当前回调验证切换会话后旧会话的接管响应不会覆盖新会话。 */ '切换会话时丢弃旧会话响应', /* 当前回调验证跨会话响应隔离。 */ async () => {
    // pending 保存第一个会话尚未完成的接管读取。
    let resolveFirst: ((value: ChatHumanHandoff) => void) | null = null;
    getHandoffMock.mockImplementationOnce(/* 当前回调返回由测试控制的未完成请求。 */ () => new Promise<ChatHumanHandoff>(/* 当前回调保存外部解决函数。 */ resolve => { resolveFirst = resolve; }));
    getHandoffMock.mockResolvedValueOnce({ account_id: 'acc1', buyer_id: 'buyer2', paused_until: 0, active: false, remaining_seconds: 0 });
    // currentChatID 是当前渲染使用的会话标识；测试通过改值后重新渲染模拟会话切换。
    let currentChatID = 'chat1';
    // view 是按会话标识渲染的接管 Hook 结果。
    const view = renderHook(/* 当前回调按会话标识创建接管 Hook。 */ () => useChatHumanHandoff('acc1', handoffSession(currentChatID)));
    currentChatID = 'chat2';
    view.rerender();
    await act(/* 当前回调让第一个会话的过期响应返回。 */ async () => {
      resolveFirst?.(handoffActive(999));
      await Promise.resolve();
    });
    // 过期响应不得把第一个会话的接管状态写到第二个会话上。
    await waitFor(/* 当前回调等待第二个会话的接管状态生效。 */ () => expect(getHandoffMock).toHaveBeenCalledWith('acc1', 'chat2', expect.anything()));
    expect(view.result.current.remainingSeconds).toBe(0);
  });

  test(/* 当前回调验证切换账号后清空接管状态并取消旧请求。 */ '切换账号时清空接管状态', /* 当前回调验证跨账号状态隔离。 */ async () => {
    // currentAccountID 是当前渲染使用的账号标识；置空用于模拟账号切换。
    let currentAccountID = 'acc1';
    // view 是按账号标识渲染的接管 Hook 结果。
    const view = renderHook(/* 当前回调按账号标识创建接管 Hook。 */ () => useChatHumanHandoff(currentAccountID, handoffSession('chat1')));
    await waitFor(/* 当前回调等待首个账号接管状态读取完成。 */ () => expect(view.result.current.loading).toBe(false));
    currentAccountID = '';
    view.rerender();
    await waitFor(/* 当前回调等待空账号接管状态清零。 */ () => expect(view.result.current.handoff).toBeNull());
    expect(view.result.current.remainingSeconds).toBe(0);
  });

  test(/* 当前回调验证设置接管失败时向界面暴露错误。 */ '接管失败时展示错误', /* 当前回调验证失败错误向界面暴露。 */ async () => {
    setHandoffMock.mockRejectedValueOnce(new Error('人工接管时长必须在 1 到 1440 分钟之间'));
    // view 是被测 Hook 的渲染结果，用于断言错误信息。
    const view = renderHook(/* 当前回调为选定会话创建接管 Hook。 */ () => useChatHumanHandoff('acc1', handoffSession('chat1')));
    await waitFor(/* 当前回调等待初次读取完成。 */ () => expect(view.result.current.loading).toBe(false));
    await act(/* 当前回调触发一次注定失败的接管。 */ async () => {
      await view.result.current.start(5);
    });
    expect(view.result.current.error).toContain('人工接管时长');
    expect(view.result.current.handoff?.active).toBe(false);
  });

  test(/* 当前回调验证倒计时格式化包含分钟与两位秒数。 */ '格式化剩余时间', /* 当前回调校验倒计时文本格式。 */ () => {
    expect(formatHandoffRemaining(0)).toBe('0:00');
    expect(formatHandoffRemaining(65)).toBe('1:05');
    expect(formatHandoffRemaining(900)).toBe('15:00');
  });
});
