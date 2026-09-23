import { ChevronDown,Hand,Loader2 } from 'lucide-react';
import React, { useEffect, useRef, useState } from 'react';
import { chatHandoffDurations, formatHandoffRemaining, type ChatHumanHandoffState } from '../handoff';

/** ChatHumanHandoffButtonProps 描述会话顶栏人工接管按钮所需的状态。 */
export interface ChatHumanHandoffButtonProps {
  /** handoffState 是当前会话买家的人工接管状态与动作。 */
  handoffState: ChatHumanHandoffState;
}

/** ChatHumanHandoffButton 渲染“接管 AI 回复”按钮：未接管时选择时长，接管中显示倒计时并可提前结束。 */
export const ChatHumanHandoffButton: React.FC<ChatHumanHandoffButtonProps> = ({ handoffState }) => {
  /** menuOpen 控制时长选择菜单的可见性。 */
  const [menuOpen, setMenuOpen] = useState(false);
  /** containerRef 保存按钮与菜单的容器，用于点击外部关闭菜单。 */
  const containerRef = useRef<HTMLDivElement | null>(null);
  // active 表示当前买家是否处于人工接管中。
  const active = handoffState.handoff?.active === true && handoffState.remainingSeconds > 0;

  useEffect(/* 当前副作用在菜单展开时监听外部点击，避免菜单遮挡其它会话操作。 */ () => {
    if (!menuOpen) return undefined;
    /** handleDocumentClick 关闭点击区域之外的接管菜单。 */
    const handleDocumentClick = (event: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(event.target as Node)) setMenuOpen(false);
    };
    document.addEventListener('mousedown', handleDocumentClick);
    return /* 当前 cleanup 移除外部点击监听并收起菜单。 */ () => {
      document.removeEventListener('mousedown', handleDocumentClick);
      setMenuOpen(false);
    };
  }, [menuOpen]);

  if (active) {
    return (
      <div className="relative z-20 flex items-center gap-2" ref={containerRef}>
        <span className="inline-flex items-center gap-1.5 rounded-lg bg-amber-100 px-2.5 py-1.5 text-[11px] font-extrabold text-amber-800">
          <Hand className="h-3.5 w-3.5" />
          人工接管中 {formatHandoffRemaining(handoffState.remainingSeconds)}
        </span>
        <button
          type="button"
          onClick={/* 当前回调提前结束当前买家的人工接管。 */ () => void handoffState.stop()}
          disabled={handoffState.busy}
          className="rounded-lg bg-gray-100 px-2.5 py-1.5 text-[11px] font-extrabold text-gray-700 transition-colors hover:bg-gray-200 disabled:opacity-50"
        >
          {handoffState.busy ? '处理中...' : '结束接管'}
        </button>
      </div>
    );
  }

  return (
    <div className="relative z-20" ref={containerRef}>
      <button
        type="button"
        aria-label="接管 AI 回复"
        aria-expanded={menuOpen}
        onClick={/* 当前回调切换时长选择菜单的可见性。 */ () => setMenuOpen(/* current 是菜单当前的展开状态。 */ current => !current)}
        disabled={handoffState.loading || handoffState.busy}
        className="inline-flex items-center gap-1.5 rounded-lg border border-sky-200 bg-sky-50 px-2.5 py-1.5 text-[11px] font-extrabold text-sky-700 transition-colors hover:bg-sky-100 disabled:opacity-50"
      >
        {handoffState.busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Hand className="h-3.5 w-3.5" />}
        接管 AI 回复
        <ChevronDown className="h-3 w-3" />
      </button>
      {menuOpen && (
        <div className="absolute right-0 z-20 mt-2 w-52 rounded-xl border border-gray-200 bg-white p-2 shadow-lg">
          <p className="px-2 pb-1.5 text-[11px] font-bold text-gray-500">接管时长（仅对该买家生效）</p>
          {chatHandoffDurations.map(/* minutes 是当前可选的接管分钟数。 */ minutes => (
            <button
              key={minutes}
              type="button"
              onClick={/* 当前回调按所选分钟数接管当前买家。 */ () => {
                setMenuOpen(false);
                void handoffState.start(minutes);
              }}
              className="block w-full rounded-lg px-2 py-2 text-left text-xs font-bold text-gray-700 transition-colors hover:bg-gray-100"
            >
              接管 {minutes} 分钟
            </button>
          ))}
        </div>
      )}
      {handoffState.error && <p className="mt-1 text-[11px] font-bold text-red-600">{handoffState.error}</p>}
    </div>
  );
};

export default ChatHumanHandoffButton;
