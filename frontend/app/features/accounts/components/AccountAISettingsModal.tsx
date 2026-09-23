import { Bot,Loader2,Save,Settings,X } from 'lucide-react';
import React from 'react';
import { createPortal } from 'react-dom';
import type { AccountDetail,AIReplySettings } from '../api';

// AccountAISettingsModalProps 描述账号 AI 设置弹窗需要的状态和回调。
export interface AccountAISettingsModalProps {
  // account 是当前正在编辑 AI 设置的账号。
  account: AccountDetail;
  // settings 是账号 AI 设置编辑草稿。
  settings: AIReplySettings;
  // saving 表示 AI 设置保存请求是否正在执行。
  saving: boolean;
  // onChange 更新 AI 设置草稿。
  onChange: (settings: AIReplySettings) => void;
  // onClose 关闭 AI 设置弹窗。
  onClose: () => void;
  // onSave 保存 AI 设置并刷新账号列表。
  onSave: () => void | Promise<void>;
}

// AccountAISettingsModal 渲染账号 AI 自动回复策略编辑界面。
export const AccountAISettingsModal: React.FC<AccountAISettingsModalProps> = ({ account, settings, saving, onChange, onClose, onSave }) => {
  // updateSettings 使用最新草稿合并单个 AI 字段变化。
  const updateSettings = (patch: Partial<AIReplySettings>) => onChange({ ...settings, ...patch });
  // handleEnabledChange 切换 AI 自动回复开关。
  const handleEnabledChange = () => updateSettings(settings.ai_enabled ? { ai_enabled: false, auto_adjust_price_enabled: false } : { ai_enabled: true });
  // handleAutoAdjustChange 切换真实订单自动改价开关，AI 议价关闭时不允许单独开启。
  const handleAutoAdjustChange = () => {
    if (!settings.ai_enabled) return;
    updateSettings({ auto_adjust_price_enabled: !settings.auto_adjust_price_enabled });
  };
  // handleDiscountPercentChange 更新最大折扣比例。
  const handleDiscountPercentChange = (event: React.ChangeEvent<HTMLInputElement>) => updateSettings({ max_discount_percent: parseInt(event.target.value, 10) || 0 });
  // handleDiscountAmountChange 更新最大折扣金额。
  const handleDiscountAmountChange = (event: React.ChangeEvent<HTMLInputElement>) => updateSettings({ max_discount_amount: parseInt(event.target.value, 10) || 0 });
  // handleBargainRoundsChange 更新最大砍价轮次。
  const handleBargainRoundsChange = (event: React.ChangeEvent<HTMLInputElement>) => updateSettings({ max_bargain_rounds: parseInt(event.target.value, 10) || 1 });
  // handleHumanHandoffMinutesChange 限制人工接管后的买家级 AI 暂停时长在 0 至 1440 分钟。
  const handleHumanHandoffMinutesChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    // value 是用户输入的人工接管暂停分钟数，空值和非法值归一为关闭。
    const value = Number.parseInt(event.target.value, 10);
    updateSettings({ human_handoff_minutes: Number.isFinite(value) ? Math.min(1440, Math.max(0, value)) : 0 });
  };
  // handleVisionToggle 切换是否把买家图片发送给多模态模型。
  const handleVisionToggle = () => updateSettings({ ai_vision_enabled: !settings.ai_vision_enabled });
  // handlePromptChange 更新砍价模式自定义 AI 提示词。
  const handlePromptChange = (event: React.ChangeEvent<HTMLTextAreaElement>) => updateSettings({ custom_prompts: event.target.value });
  // handleFullPromptChange 更新关键词优先或完全接管模式的独立系统提示词。
  const handleFullPromptChange = (event: React.ChangeEvent<HTMLTextAreaElement>) => updateSettings({ ai_full_prompt: event.target.value });
  // handleReplyModeChange 切换 AI 回复模式并保留其他账号级策略。
  const handleReplyModeChange = (mode: AIReplySettings['ai_reply_mode']) => updateSettings({ ai_reply_mode: mode });

  return createPortal(
    <div className="modal-overlay-centered">
      <div className="modal-container" style={{ maxWidth: '600px' }}>
        <div className="modal-header">
          <div>
            <h3 className="text-2xl font-extrabold text-gray-900 flex items-center gap-2"><Bot className="w-6 h-6 text-purple-500" />AI助手设置</h3>
            <p className="text-sm text-gray-500 mt-1">{account.nickname || account.remark || account.id}</p>
          </div>
          <button onClick={onClose} className="p-2 rounded-xl hover:bg-gray-100 transition-colors flex-shrink-0" aria-label="关闭 AI 设置"><X className="w-5 h-5 text-gray-500" /></button>
        </div>

        <div className="modal-body space-y-6">
          <div className="flex items-center justify-between p-4 bg-purple-50 rounded-xl">
            <div><div className="font-bold text-gray-900 flex items-center gap-2"><Bot className="w-4 h-4 text-purple-500" />启用AI自动回复</div><div className="text-xs text-gray-500">AI 将按下方模式自动回答买家消息</div></div>
            <button type="button" onClick={handleEnabledChange} className={`w-14 h-8 rounded-full transition-colors duration-300 relative ${settings.ai_enabled ? 'bg-brand' : 'bg-gray-300'}`} aria-label="切换 AI 自动回复">
              <span className={`absolute left-1 top-1 w-6 h-6 bg-white rounded-full shadow-md transition-transform duration-300 ${settings.ai_enabled ? 'translate-x-6' : 'translate-x-0'}`} />
            </button>
          </div>

          <div className="flex items-center justify-between p-4 bg-amber-50 border border-amber-200 rounded-xl">
            <div className="pr-4"><div className="font-bold text-gray-900">自动执行 AI 报价改价</div><div className="text-xs text-gray-600 mt-1">AI 明确报价后，买家在 30 分钟内拍下对应商品时自动修改待付款订单价格。开启 AI 议价后，固定自动化规则改价将不能同时启用。</div></div>
            <button type="button" onClick={handleAutoAdjustChange} disabled={!settings.ai_enabled} className={`w-14 h-8 rounded-full transition-colors duration-300 relative flex-shrink-0 ${settings.auto_adjust_price_enabled ? 'bg-amber-500' : 'bg-gray-300'} ${!settings.ai_enabled ? 'opacity-50 cursor-not-allowed' : ''}`} aria-label="切换 AI 自动改价">
              <span className={`absolute left-1 top-1 w-6 h-6 bg-white rounded-full shadow-md transition-transform duration-300 ${settings.auto_adjust_price_enabled ? 'translate-x-6' : 'translate-x-0'}`} />
            </button>
          </div>

          <div className="border-t border-gray-200 pt-6">
            <h3 className="text-lg font-bold text-gray-900 mb-4">回复模式</h3>
            <div className="grid grid-cols-3 gap-3">
              <button type="button" onClick={/* 当前回调切换到仅砍价 AI 回复模式。 */ () => handleReplyModeChange('bargain')} className={`p-3 rounded-xl border text-left ${settings.ai_reply_mode === 'bargain' ? 'border-purple-500 bg-purple-50' : 'border-gray-200'}`}>
                <div className="font-bold text-sm">仅砍价</div><div className="text-xs text-gray-500 mt-1">只处理砍价问题</div>
              </button>
              <button type="button" onClick={/* 当前回调切换到关键词优先 AI 回复模式。 */ () => handleReplyModeChange('keyword_first')} className={`p-3 rounded-xl border text-left ${settings.ai_reply_mode === 'keyword_first' ? 'border-purple-500 bg-purple-50' : 'border-gray-200'}`}>
                <div className="font-bold text-sm">关键词优先</div><div className="text-xs text-gray-500 mt-1">规则优先，AI 回答其他问题</div>
              </button>
              <button type="button" onClick={/* 当前回调切换到 AI 完全接管回复模式。 */ () => handleReplyModeChange('full')} className={`p-3 rounded-xl border text-left ${settings.ai_reply_mode === 'full' ? 'border-purple-500 bg-purple-50' : 'border-gray-200'}`}>
                <div className="font-bold text-sm">AI 完全接管</div><div className="text-xs text-gray-500 mt-1">所有问题都由 AI 回答</div>
              </button>
            </div>
          </div>

          {settings.ai_reply_mode !== 'bargain' && <div><label className="block text-sm font-bold text-gray-700 mb-2">完全模式提示词（可选）</label><textarea value={settings.ai_full_prompt} onChange={handleFullPromptChange} placeholder="可使用 {item_title}、{item_price}、{item_description} 占位符；留空使用默认提示词" className="w-full ios-input px-4 py-3 rounded-xl h-32 resize-none" /></div>}

          <div className="border-t border-gray-200 pt-6">
            <h3 className="text-lg font-bold text-gray-900 mb-4">砍价策略</h3>
            <div className="grid grid-cols-3 gap-4">
              <div><label className="block text-sm font-bold text-gray-700 mb-2">最大折扣比例 (%)</label><input type="number" value={settings.max_discount_percent} onChange={handleDiscountPercentChange} className="w-full ios-input px-4 py-3 rounded-xl" min="0" max="100" /><p className="text-xs text-gray-500 mt-1">例如：10 表示最多降价 10%；设为 0 表示不允许降价</p></div>
              <div><label className="block text-sm font-bold text-gray-700 mb-2">最大折扣金额 (元)</label><input type="number" value={settings.max_discount_amount} onChange={handleDiscountAmountChange} className="w-full ios-input px-4 py-3 rounded-xl" min="0" /><p className="text-xs text-gray-500 mt-1">例如：100 表示最多降价 100 元；设为 0 表示不允许降价</p></div>
              <div><label className="block text-sm font-bold text-gray-700 mb-2">最大砍价轮次</label><input type="number" value={settings.max_bargain_rounds} onChange={handleBargainRoundsChange} className="w-full ios-input px-4 py-3 rounded-xl" min="1" max="10" /><p className="text-xs text-gray-500 mt-1">买家最多可以砍价的次数</p></div>
            </div>
            <div className="mt-4"><label htmlFor="human-handoff-minutes" className="block text-sm font-bold text-gray-700 mb-2">人工接管后暂停 AI（分钟）</label><input id="human-handoff-minutes" type="number" value={settings.human_handoff_minutes} onChange={handleHumanHandoffMinutesChange} className="w-full ios-input px-4 py-3 rounded-xl" min="0" max="1440" /><p className="text-xs text-gray-500 mt-1">买家发送人工后暂停该买家 AI；设为 0 关闭</p></div>

            <div className="mt-6 flex items-center justify-between p-4 bg-sky-50 border border-sky-200 rounded-xl">
              <div className="pr-4">
                <div className="font-bold text-gray-900">识别买家图片</div>
                <div className="text-xs text-gray-600 mt-1">买家发来图片时会先下载并随消息一起发送给你配置的模型服务；关闭后只按文字回复。图片识别需要模型本身支持视觉输入，模型不支持时会自动退回纯文字。</div>
              </div>
              <button type="button" onClick={handleVisionToggle} className={`w-14 h-8 rounded-full transition-colors duration-300 relative flex-shrink-0 ${settings.ai_vision_enabled ? 'bg-sky-500' : 'bg-gray-300'}`} aria-label="切换买家图片识别">
                <span className={`absolute left-1 top-1 w-6 h-6 bg-white rounded-full shadow-md transition-transform duration-300 ${settings.ai_vision_enabled ? 'translate-x-6' : 'translate-x-0'}`} />
              </button>
            </div>
          </div>

          <div><label className="block text-sm font-bold text-gray-700 mb-2">自定义提示词（可选）</label><textarea value={settings.custom_prompts} onChange={handlePromptChange} placeholder="输入自定义的AI回复规则或风格指引...&#10;&#10;例如：回复时保持礼貌专业、使用简洁的语言、强调产品质量等" className="w-full ios-input px-4 py-3 rounded-xl h-40 resize-none" /></div>
          <div className="bg-blue-50 border border-blue-200 rounded-xl p-4">
            <h4 className="font-bold text-blue-900 mb-2 flex items-center gap-2"><Settings className="w-4 h-4" />AI如何工作</h4>
            <ul className="text-xs text-blue-800 space-y-1"><li>• 自动识别买家的砍价请求</li><li>• 根据设定的策略智能回复</li><li>• 在合理范围内同意降价或礼貌拒绝</li><li>• 只有开启自动改价后，已发送给买家的有效报价才会用于真实订单改价</li></ul>
          </div>
        </div>

        <div className="modal-footer"><div className="flex gap-3 w-full">
          <button onClick={onClose} className="flex-1 px-6 py-3 rounded-xl font-bold bg-gray-100 text-gray-700 hover:bg-gray-200 transition-colors" disabled={saving}>取消</button>
          <button onClick={/* 当前回调处理用户交互或异步状态变化。 */ () => void onSave()} className="flex-1 ios-btn-primary px-6 py-3 rounded-xl font-bold flex items-center justify-center gap-2" disabled={saving}>{saving ? <Loader2 className="w-4 h-4 animate-spin" /> : <Save className="w-4 h-4" />}{saving ? '保存中...' : '保存'}</button>
        </div></div>
      </div>
    </div>,
    document.body,
  );
};
