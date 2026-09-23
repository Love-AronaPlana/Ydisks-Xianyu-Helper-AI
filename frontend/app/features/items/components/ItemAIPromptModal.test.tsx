// @vitest-environment jsdom
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, test, vi } from 'vitest';

/** getPromptMock 是弹窗读取商品提示词的 API 替身。 */
const { getPromptMock, updatePromptMock } = vi.hoisted(/* promptApiMockFactory 创建弹窗请求替身。 */ () => ({ getPromptMock: vi.fn(), updatePromptMock: vi.fn() }));
vi.mock('../api', /* itemPromptApiModuleMockFactory 提供弹窗测试所需请求替身。 */ () => ({ getItemAIPrompt: getPromptMock, updateItemAIPrompt: updatePromptMock }));

import { ItemAIPromptModal } from './ItemAIPromptModal';

/** itemFixture 是弹窗展示使用的最小商品 UI 模型。 */
const itemFixture = { id: 'local-1', cookie_id: 'account-1', item_id: 'item-1', item_title: '测试商品', item_price: '9.9', item_description: '描述', item_category: '资料' };

describe('ItemAIPromptModal', /* 测试商品提示词编辑器的加载、编辑和保存。 */ () => {
  beforeEach(/* 当前回调重置 API 替身并设置默认成功响应。 */ () => {
    vi.clearAllMocks();
    getPromptMock.mockResolvedValue({ strategy: 'inherit', prompt: '默认', custom_variables: { tone: '礼貌' }, configured: true });
    updatePromptMock.mockResolvedValue({ cookie_id: 'account-1', item_id: 'item-1', strategy: 'append', prompt: '默认', custom_variables: { tone: '简洁' }, configured: true });
  });

  test('加载配置、插入内置变量、编辑变量并保存', /* 当前回调覆盖商品提示词编辑主路径。 */ async () => {
    /** onClose 保存测试中弹窗关闭后的调用记录。 */
    const onClose = vi.fn();
    render(<ItemAIPromptModal item={itemFixture} onClose={onClose} />);
    await waitFor(/* 当前回调等待服务端配置进入商品提示词表单。 */ () => expect(screen.getByDisplayValue('默认')).toBeTruthy());
    fireEvent.change(screen.getByLabelText('提示词策略'), { target: { value: 'append' } });
    fireEvent.click(screen.getByRole('button', { name: '插入 {item_title}' }));
    fireEvent.change(screen.getByLabelText('变量值 1'), { target: { value: '简洁' } });
    fireEvent.click(screen.getByRole('button', { name: '保存' }));
    await waitFor(/* 当前回调等待保存请求完成并核对复合键和载荷。 */ () => expect(updatePromptMock).toHaveBeenCalledWith('account-1', 'item-1', expect.objectContaining({ strategy: 'append', prompt: expect.stringContaining('{item_title}'), custom_variables: { tone: '简洁' } }), expect.objectContaining({ signal: expect.any(AbortSignal) })));
    expect(onClose).toHaveBeenCalledOnce();
  });

  test('加载失败展示错误且不提交', /* 当前回调覆盖读取错误分支。 */ async () => {
    getPromptMock.mockRejectedValueOnce(new Error('读取失败'));
    render(<ItemAIPromptModal item={itemFixture} onClose={vi.fn()} />);
    await waitFor(/* 当前回调等待读取错误展示在弹窗中。 */ () => expect(screen.getByRole('alert').textContent).toContain('读取失败'));
    expect(updatePromptMock).not.toHaveBeenCalled();
  });
});
