// @vitest-environment jsdom
import { cleanup,fireEvent,render,screen } from '@testing-library/react';
import { afterEach,describe,expect,test,vi } from 'vitest';
import { ChatHumanHandoffButton } from './ChatHumanHandoffButton';
import type { ChatHumanHandoffState } from '../handoff';

afterEach(/* 当前回调卸载上一次渲染的按钮，避免多次渲染互相干扰。 */ () => cleanup());

/** handoffStateFixture 生成指定状态的接管按钮测试输入。 */
const handoffStateFixture = (patch: Partial<ChatHumanHandoffState> = {}): ChatHumanHandoffState => ({
  handoff: null,
  remainingSeconds: 0,
  loading: false,
  busy: false,
  error: '',
  start: vi.fn(),
  stop: vi.fn(),
  ...patch,
});

describe(/* 当前回调覆盖接管按钮的时长选择、倒计时与提前结束。 */ 'ChatHumanHandoffButton', () => {
  test(/* 当前回调验证选择 5/10/15 分钟后按所选分钟发起接管。 */ '选择时长后发起接管', /* 当前回调验证时长选择回调。 */ () => {
    // start 记录按钮发起的接管分钟数。
    const start = vi.fn();
    render(<ChatHumanHandoffButton handoffState={handoffStateFixture({ start })} />);
    fireEvent.click(screen.getByLabelText('接管 AI 回复'));
    fireEvent.click(screen.getByText('接管 10 分钟'));
    expect(start).toHaveBeenCalledWith(10);
  });

  test(/* 当前回调验证接管中显示倒计时并提供提前结束。 */ '接管中显示倒计时并可提前结束', /* 当前回调验证接管中展示与提前结束。 */ () => {
    // stop 记录提前结束接管的调用次数。
    const stop = vi.fn();
    render(<ChatHumanHandoffButton handoffState={handoffStateFixture({
      handoff: { account_id: 'acc1', buyer_id: 'buyer1', paused_until: 1_700_000_300, active: true, remaining_seconds: 300 },
      remainingSeconds: 300,
      stop,
    })} />);
    expect(screen.getByText('人工接管中 5:00')).toBeTruthy();
    fireEvent.click(screen.getByText('结束接管'));
    expect(stop).toHaveBeenCalledTimes(1);
  });

  test(/* 当前回调验证接管请求进行中禁用按钮，避免重复提交。 */ '请求进行中禁用按钮', /* 当前回调验证请求进行中按钮被禁用。 */ () => {
    render(<ChatHumanHandoffButton handoffState={handoffStateFixture({ busy: true })} />);
    // button 是接管按钮元素，用于断言禁用状态。
    const button = screen.getByLabelText('接管 AI 回复') as HTMLButtonElement;
    expect(button.disabled).toBe(true);
  });

  test(/* 当前回调验证失败说明展示在按钮下方。 */ '失败时展示错误说明', /* 当前回调验证失败说明可见。 */ () => {
    render(<ChatHumanHandoffButton handoffState={handoffStateFixture({ error: '设置人工接管失败' })} />);
    expect(screen.getByText('设置人工接管失败')).toBeTruthy();
  });
});
