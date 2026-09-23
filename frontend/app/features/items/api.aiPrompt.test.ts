import { beforeEach, describe, expect, test, vi } from 'vitest';

/** postMock 保存商品提示词测试中的契约客户端 POST 替身。 */
const { getMock, putMock } = vi.hoisted(/* apiMockFactory 创建商品提示词接口替身。 */ () => ({ getMock: vi.fn(), putMock: vi.fn() }));

vi.mock('../../../shared/api-contract/client', /* contractClientModuleMockFactory 提供商品提示词 adapter 所需契约依赖。 */ () => ({
  /** contractClientMock 仅提供本测试覆盖的 GET 与 PUT 操作。 */
  contractClient: { GET: getMock, PUT: putMock },
  /** runContractRequestMock 执行 adapter 构造的请求并返回 data。 */
  runContractRequest: async (/* execute 是商品提示词 adapter 构造的契约请求动作。 */ execute: (signal: AbortSignal) => Promise<{ /** data 是契约客户端返回的成功响应。 */ data?: unknown }>) => (await execute(new AbortController().signal)).data,
}));

import { contractClient } from '../../../shared/api-contract/client';
import { getItemAIPrompt, updateItemAIPrompt } from './api';

/** getPromptMock 是读取提示词配置的类型化测试替身。 */
const getPromptMock = vi.mocked(contractClient.GET);
/** putPromptMock 是保存提示词配置的类型化测试替身。 */
const putPromptMock = vi.mocked(contractClient.PUT);

describe('商品 AI 提示词 adapter', /* 测试商品提示词路径和请求字段映射。 */ () => {
  beforeEach(/* 当前回调重置请求替身。 */ () => {
    vi.clearAllMocks();
    getPromptMock.mockResolvedValue({ data: { cookie_id: 'account-1', item_id: 'item-1', strategy: 'inherit', prompt: '', custom_variables: {}, configured: false } } as never);
    putPromptMock.mockResolvedValue({ data: { cookie_id: 'account-1', item_id: 'item-1', strategy: 'append', prompt: '礼貌', custom_variables: { tone: '简洁' }, configured: true } } as never);
  });

  test('读取配置使用账号和商品复合键', /* 当前回调验证 GET 的路径参数和取消信号。 */ async () => {
    await getItemAIPrompt('account-1', 'item-1', { signal: new AbortController().signal });
    expect(getPromptMock).toHaveBeenCalledWith('/api/v1/items/{cookie_id}/{item_id}/ai-prompt', expect.objectContaining({ params: { path: { cookie_id: 'account-1', item_id: 'item-1' } }, signal: expect.any(AbortSignal) }));
  });

  test('保存配置发送策略、提示词和自定义变量', /* 当前回调验证 PUT 保留后端约定字段。 */ async () => {
    await updateItemAIPrompt('account-1', 'item-1', { strategy: 'append', prompt: '礼貌', custom_variables: { tone: '简洁' } });
    expect(putPromptMock).toHaveBeenCalledWith('/api/v1/items/{cookie_id}/{item_id}/ai-prompt', expect.objectContaining({ params: { path: { cookie_id: 'account-1', item_id: 'item-1' } }, body: { strategy: 'append', prompt: '礼貌', custom_variables: { tone: '简洁' } }, signal: expect.any(AbortSignal) }));
  });
});
