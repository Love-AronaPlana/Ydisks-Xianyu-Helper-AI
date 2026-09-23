import { beforeEach, describe, expect, test, vi } from 'vitest';

/** getMock 保存账号 AI 设置读取请求的契约客户端替身。 */
const { getMock, putMock } = vi.hoisted(/* apiMockFactory 创建账号 AI 设置接口替身。 */ () => ({ getMock: vi.fn(), putMock: vi.fn() }));

vi.mock('../../../shared/api-contract/client', /* contractClientModuleMockFactory 提供账号 AI adapter 所需契约依赖。 */ () => ({
  /** contractClientMock 只提供当前测试覆盖的 GET 与 PUT 操作。 */
  contractClient: { GET: getMock, PUT: putMock },
  /** runContractRequestMock 执行 adapter 构造的请求并返回契约响应数据。 */
  runContractRequest: async (/* execute 是账号 AI adapter 构造的契约请求动作。 */ execute: (signal: AbortSignal) => Promise<{ /** data 是契约客户端返回的成功响应。 */ data?: unknown }>) => (await execute(new AbortController().signal)).data,
}));

import { contractClient } from '../../../shared/api-contract/client';
import { getAccountAISettings, updateAccountAISettings } from './api';

/** getSettingsMock 是账号 AI 设置读取的类型化测试替身。 */
const getSettingsMock = vi.mocked(contractClient.GET);
/** updateSettingsMock 是账号 AI 设置保存的类型化测试替身。 */
const updateSettingsMock = vi.mocked(contractClient.PUT);

describe('账号 AI 设置 adapter', /* 测试人工接管暂停字段的读取、保存和取消信号透传。 */ () => {
  beforeEach(/* 当前回调重置账号 AI 请求替身。 */ () => {
    vi.clearAllMocks();
    getSettingsMock.mockResolvedValue({ data: { cookie_id: 'account-1', ai_enabled: true, auto_adjust_price_enabled: false, ai_reply_mode: 'bargain', ai_full_prompt: '', human_handoff_minutes: 90, max_discount_percent: 10, max_discount_amount: 100, max_bargain_rounds: 3, custom_prompts: '' } } as never);
    updateSettingsMock.mockResolvedValue({ data: { success: true } } as never);
  });

  test('读取人工接管暂停时长并透传取消信号', /* 当前回调验证读取字段和取消信号透传。 */ async () => {
    // controller 提供本次读取请求的外部取消信号。
    const controller = new AbortController();
    // settings 保存 adapter 归一后的账号 AI 设置响应。
    const settings = await getAccountAISettings('account-1', { signal: controller.signal });
    expect(settings.human_handoff_minutes).toBe(90);
    expect(getSettingsMock).toHaveBeenCalledWith('/api/v1/settings/ai-reply/{cookie_id}', expect.objectContaining({ params: { path: { cookie_id: 'account-1' } }, signal: expect.any(AbortSignal) }));
  });

  test('保存时包含人工接管暂停字段并保留现有 AI 字段', /* 当前回调验证保存载荷保留全部 AI 策略。 */ async () => {
    await updateAccountAISettings('account-1', { ai_enabled: true, auto_adjust_price_enabled: true, ai_reply_mode: 'keyword_first', ai_full_prompt: '请简洁回答', human_handoff_minutes: 1440, ai_vision_enabled: true, max_discount_percent: 20, max_discount_amount: 80, max_bargain_rounds: 4, custom_prompts: '礼貌' });
    expect(updateSettingsMock).toHaveBeenCalledWith('/api/v1/settings/ai-reply/{cookie_id}', expect.objectContaining({
      params: { path: { cookie_id: 'account-1' } },
      body: { ai_enabled: true, auto_adjust_price_enabled: true, ai_reply_mode: 'keyword_first', ai_full_prompt: '请简洁回答', human_handoff_minutes: 1440, ai_vision_enabled: true, max_discount_percent: 20, max_discount_amount: 80, max_bargain_rounds: 4, custom_prompts: '礼貌' },
      signal: expect.any(AbortSignal),
    }));
  });

  test('缺少人工接管暂停字段时保存默认关闭值', /* 当前回调验证旧调用方获得关闭人工接管暂停的默认值。 */ async () => {
    await updateAccountAISettings('account-1', { ai_enabled: false });
    expect(updateSettingsMock.mock.calls[0]?.[1]).toEqual(expect.objectContaining({ body: expect.objectContaining({ human_handoff_minutes: 0 }) }));
  });

  test('未显式指定时默认开启买家图片识别', /* 当前回调验证图片识别默认开启且不会因省略字段被关闭。 */ async () => {
    await updateAccountAISettings('account-1', { ai_enabled: true });
    expect(updateSettingsMock.mock.calls[0]?.[1]).toEqual(expect.objectContaining({ body: expect.objectContaining({ ai_vision_enabled: true }) }));
  });
});
