import { beforeEach, describe, expect, test, vi } from 'vitest';

/** getMock、putMock、deleteMock 保存人工接管契约客户端替身。 */
const { getMock, putMock, deleteMock } = vi.hoisted(/* apiMockFactory 创建人工接管接口替身。 */ () => ({ getMock: vi.fn(), putMock: vi.fn(), deleteMock: vi.fn() }));

vi.mock('../../../shared/api-contract/client', /* contractClientModuleMockFactory 提供聊天 adapter 所需契约依赖。 */ () => ({
  /** contractClientMock 只提供当前测试覆盖的 GET、PUT 与 DELETE 操作。 */
  contractClient: { GET: getMock, PUT: putMock, DELETE: deleteMock },
  /** contractMultipartBodyMock 在本用例中不会被调用，仅保持模块形状一致。 */
  contractMultipartBody: (body: unknown) => body,
  /** runContractRequestMock 执行 adapter 构造的请求并返回契约响应数据。 */
  runContractRequest: /* 当前回调立即执行契约请求动作并返回其响应。 */ async (/* execute 是聊天 adapter 构造的契约请求动作。 */ execute: (/* signal 是本次契约请求的取消信号。 */ signal: AbortSignal) => Promise<unknown>) => execute(new AbortController().signal),
}));

import { contractClient } from '../../../shared/api-contract/client';
import { clearChatHumanHandoff, getChatHumanHandoff, setChatHumanHandoff } from './api';

/** getHandoffMock 是人工接管读取的类型化测试替身。 */
const getHandoffMock = vi.mocked(contractClient.GET);
/** setHandoffMock 是人工接管设置的类型化测试替身。 */
const setHandoffMock = vi.mocked(contractClient.PUT);
/** clearHandoffMock 是人工接管结束的类型化测试替身。 */
const clearHandoffMock = vi.mocked(contractClient.DELETE);

describe(/* 当前回调覆盖人工接管 adapter 的读取、设置与结束。 */ '聊天人工接管 adapter', () => {
  beforeEach(/* 当前回调重置人工接管请求替身。 */ () => {
    vi.clearAllMocks();
    getHandoffMock.mockResolvedValue({ account_id: 'acc1', buyer_id: 'buyer1', paused_until: 0, active: false, remaining_seconds: 0 } as never);
    setHandoffMock.mockResolvedValue({ account_id: 'acc1', buyer_id: 'buyer1', paused_until: 1_700_000_300, active: true, remaining_seconds: 300 } as never);
    clearHandoffMock.mockResolvedValue({ account_id: 'acc1', buyer_id: 'buyer1', paused_until: 0, active: false, remaining_seconds: 0 } as never);
  });

  test(/* 当前回调验证读取请求携带账号与会话查询参数。 */ '读取接管状态时传递账号与会话', /* 当前回调执行一次接管状态读取。 */ async () => {
    // state 是 adapter 归一后的接管状态。
    const state = await getChatHumanHandoff('acc1', 'chat1');
    expect(state.active).toBe(false);
    expect(getHandoffMock).toHaveBeenCalledWith('/api/v1/chat/human-handoff', expect.objectContaining({ params: { query: { account_id: 'acc1', chat_id: 'chat1' } } }));
  });

  test(/* 当前回调验证设置请求把分钟数放入请求体。 */ '设置接管时按分钟提交', /* 当前回调执行一次按分钟接管设置。 */ async () => {
    // state 是设置成功后的接管状态。
    const state = await setChatHumanHandoff('acc1', 'chat1', 5);
    expect(state.remaining_seconds).toBe(300);
    expect(setHandoffMock).toHaveBeenCalledWith('/api/v1/chat/human-handoff', expect.objectContaining({ body: { account_id: 'acc1', chat_id: 'chat1', minutes: 5 } }));
  });

  test(/* 当前回调验证结束接管请求走 DELETE 并携带隔离键。 */ '结束接管时传递账号与会话', /* 当前回调执行一次提前结束接管。 */ async () => {
    // state 是结束后的接管状态；服务端返回未接管。
    const state = await clearChatHumanHandoff('acc1', 'chat1');
    expect(state.active).toBe(false);
    expect(clearHandoffMock).toHaveBeenCalledWith('/api/v1/chat/human-handoff', expect.objectContaining({ params: { query: { account_id: 'acc1', chat_id: 'chat1' } } }));
  });
});
