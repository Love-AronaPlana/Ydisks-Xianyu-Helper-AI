import { useCallback, useEffect, useRef, useState } from 'react';
import { clearChatHumanHandoff, getChatHumanHandoff, setChatHumanHandoff } from './api';
import type { ChatHumanHandoff, ChatSession } from './models';
import { isChatAbortError } from './state';

/** chatHandoffDurations 是界面提供的人工接管时长选项，单位为分钟。 */
export const chatHandoffDurations = [5, 10, 15] as const;

/** ChatHumanHandoffState 描述会话顶栏人工接管按钮的状态与动作。 */
export type ChatHumanHandoffState = {
  /** handoff 保存当前选中会话买家的接管状态；未接管或未选中会话时为 null。 */
  handoff: ChatHumanHandoff | null;
  /** remainingSeconds 是本地递减的剩余接管秒数，用于界面倒计时。 */
  remainingSeconds: number;
  /** loading 表示接管状态正在读取。 */
  loading: boolean;
  /** busy 表示接管设置或提前结束请求正在进行。 */
  busy: boolean;
  /** error 保存接管操作的用户可见失败说明。 */
  error: string;
  /** start 按指定分钟数接管当前会话买家的 AI 回复。 */
  start: (minutes: number) => Promise<void>;
  /** stop 提前结束当前会话买家的人工接管。 */
  stop: () => Promise<void>;
};

/** useChatHumanHandoff 管理单个会话买家的人工接管状态，并按账号与会话隔离过期响应。 */
export const useChatHumanHandoff = (activeAccountID: string, selectedSession: ChatSession | null): ChatHumanHandoffState => {
  /** handoff 保存服务端返回的接管状态。 */
  const [handoff, setHandoff] = useState<ChatHumanHandoff | null>(null);
  /** remainingSeconds 保存本地倒计时的剩余秒数。 */
  const [remainingSeconds, setRemainingSeconds] = useState(0);
  /** loading 表示接管状态读取中。 */
  const [loading, setLoading] = useState(false);
  /** busy 表示接管设置或结束请求进行中。 */
  const [busy, setBusy] = useState(false);
  /** error 保存接管操作的失败说明。 */
  const [error, setError] = useState('');
  /** requestSequenceRef 为接管请求分配单调递增代次，阻止旧会话响应覆盖新会话。 */
  const requestSequenceRef = useRef(0);
  /** requestControllerRef 保存当前接管请求的取消控制器。 */
  const requestControllerRef = useRef<AbortController | null>(null);

  /** chatID 是当前选中会话标识；没有会话时不发起请求。 */
  const chatID = selectedSession?.chat_id || '';

  // 应用服务端返回的接管结果，并把倒计时重置为服务端给出的剩余秒数。
  const applyHandoff = useCallback(/* 当前回调把服务端接管状态写入本地倒计时。 */ (next: ChatHumanHandoff) => {
    setHandoff(next);
    setRemainingSeconds(next.active ? Math.max(0, next.remaining_seconds) : 0);
  }, []);

  useEffect(/* 当前副作用在账号或会话切换时读取接管状态，并在 cleanup 中取消旧请求。 */ () => {
    requestControllerRef.current?.abort();
    // sequence 保存本次接管查询的请求代次。
    const sequence = ++requestSequenceRef.current;
    if (!activeAccountID || !chatID) {
      setHandoff(null);
      setRemainingSeconds(0);
      setLoading(false);
      setError('');
      return undefined;
    }
    setHandoff(null);
    setRemainingSeconds(0);
    setError('');
    setLoading(true);
    // controller 保存本次接管查询的取消控制器。
    const controller = new AbortController();
    requestControllerRef.current = controller;
    void getChatHumanHandoff(activeAccountID, chatID, { signal: controller.signal }).then(/* next 保存服务端返回的接管状态。 */ next => {
      if (controller.signal.aborted || sequence !== requestSequenceRef.current) return;
      applyHandoff(next);
    }).catch(/* loadError 保存接管状态读取失败原因；主动取消不显示错误。 */ loadError => {
      if (controller.signal.aborted || sequence !== requestSequenceRef.current || isChatAbortError(loadError)) return;
      setError(loadError instanceof Error ? loadError.message : '读取人工接管状态失败');
    }).finally(/* 当前回调在仍为最新请求时结束加载状态。 */ () => {
      if (controller.signal.aborted || sequence !== requestSequenceRef.current) return;
      setLoading(false);
    });
    return /* 当前 cleanup 取消会话或账号切换后不再有效的接管查询。 */ () => controller.abort();
  }, [activeAccountID, applyHandoff, chatID]);

  useEffect(/* 当前副作用为接管中的会话维护本地倒计时，归零后自动结束接管显示。 */ () => {
    if (remainingSeconds <= 0) return undefined;
    // timer 是每秒递减剩余时间的一次性定时器。
    const timer = window.setTimeout(/* 当前回调把倒计时推进一秒。 */ () => setRemainingSeconds(/* current 是上一秒的剩余秒数。 */ current => Math.max(0, current - 1)), 1000);
    return /* 当前 cleanup 清理解释倒计时定时器，避免卸载后写入状态。 */ () => window.clearTimeout(timer);
  }, [remainingSeconds]);

  /** start 按分钟数请求接管当前会话买家的 AI 回复。 */
  const start = useCallback(/* 当前回调按所选分钟数接管当前会话买家。 */ async (minutes: number): Promise<void> => {
    if (!activeAccountID || !chatID) return;
    requestControllerRef.current?.abort();
    // sequence 保存本次接管设置请求的代次。
    const sequence = ++requestSequenceRef.current;
    // controller 保存本次接管设置请求的取消控制器。
    const controller = new AbortController();
    requestControllerRef.current = controller;
    setBusy(true);
    setError('');
    try {
      // next 保存设置后的接管状态。
      const next = await setChatHumanHandoff(activeAccountID, chatID, minutes, { signal: controller.signal });
      if (controller.signal.aborted || sequence !== requestSequenceRef.current) return;
      applyHandoff(next);
    } catch (/* startErr 保存接管设置失败原因；主动取消不显示错误。 */ startErr) {
      if (controller.signal.aborted || sequence !== requestSequenceRef.current || isChatAbortError(startErr)) return;
      setError(startErr instanceof Error ? startErr.message : '设置人工接管失败');
    } finally {
      if (!controller.signal.aborted && sequence === requestSequenceRef.current) setBusy(false);
    }
  }, [activeAccountID, applyHandoff, chatID]);

  /** stop 提前结束当前会话买家的人工接管。 */
  const stop = useCallback(/* 当前回调提前结束当前会话买家的人工接管。 */ async (): Promise<void> => {
    if (!activeAccountID || !chatID) return;
    requestControllerRef.current?.abort();
    // sequence 保存本次接管结束请求的代次。
    const sequence = ++requestSequenceRef.current;
    // controller 保存本次接管结束请求的取消控制器。
    const controller = new AbortController();
    requestControllerRef.current = controller;
    setBusy(true);
    setError('');
    try {
      // next 保存结束后的接管状态；服务端返回未接管状态。
      const next = await clearChatHumanHandoff(activeAccountID, chatID, { signal: controller.signal });
      if (controller.signal.aborted || sequence !== requestSequenceRef.current) return;
      applyHandoff(next);
    } catch (/* stopErr 保存结束接管失败原因；主动取消不显示错误。 */ stopErr) {
      if (controller.signal.aborted || sequence !== requestSequenceRef.current || isChatAbortError(stopErr)) return;
      setError(stopErr instanceof Error ? stopErr.message : '结束人工接管失败');
    } finally {
      if (!controller.signal.aborted && sequence === requestSequenceRef.current) setBusy(false);
    }
  }, [activeAccountID, applyHandoff, chatID]);

  useEffect(/* 当前副作用在 Hook 卸载时取消仍在执行的人工接管请求。 */ () => /* 卸载回调使所有未完成的接管响应失效。 */ () => {
    requestControllerRef.current?.abort();
    requestSequenceRef.current += 1;
  }, []);

  return {
    handoff,
    remainingSeconds,
    loading,
    busy,
    error,
    start,
    stop,
  };
};

/** formatHandoffRemaining 把剩余秒数格式化为“分:秒”，供接管按钮展示倒计时。 */
export const formatHandoffRemaining = (seconds: number): string => {
  // safeSeconds 保存非负的剩余秒数，避免负数或小数进入展示。
  const safeSeconds = Math.max(0, Math.floor(seconds));
  // minutes 和 restSeconds 分别是倒计时的分钟部分与秒数部分。
  const minutes = Math.floor(safeSeconds / 60);
  // restSeconds 是倒计时中不足一分钟的剩余秒数，固定补齐两位展示。
  const restSeconds = safeSeconds % 60;
  return `${minutes}:${String(restSeconds).padStart(2, '0')}`;
};
