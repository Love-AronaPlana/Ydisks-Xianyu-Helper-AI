import { Loader2, Plus, Save, Trash2, X } from 'lucide-react';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { getItemAIPrompt, updateItemAIPrompt } from '../api';
import {
  createEmptyItemAIVariableRow,
  emptyItemAIPromptInput,
  itemAIBuiltInVariables,
  itemAIVariablesToRecord,
  itemAIVariablesToRows,
  type Item,
  type ItemAIPromptInput,
  type ItemAIPromptStrategy,
  type ItemAIVariableRow,
} from '../models';

/** ItemAIPromptModalProps 描述商品 AI 提示词编辑器所需的商品和关闭回调。 */
export interface ItemAIPromptModalProps {
  /** item 是当前编辑的商品；账号和商品复合键共同决定配置归属。 */
  item: Item;
  /** onClose 关闭弹窗并让父页面清理当前复合键。 */
  onClose: () => void;
}

/** ItemAIPromptModal 编辑单个商品的 AI 提示词、继承策略和自定义变量。 */
export const ItemAIPromptModal: React.FC<ItemAIPromptModalProps> = ({ item, onClose }) => {
  /** form 保存当前商品提示词的表单草稿，不直接作为服务端 DTO 使用。 */
  const [form, setForm] = useState<ItemAIPromptInput>(emptyItemAIPromptInput);
  /** variableRows 保存可增删的自定义变量编辑行。 */
  const [variableRows, setVariableRows] = useState<ItemAIVariableRow[]>([]);
  /** loading 表示当前复合键的服务端配置读取是否进行中。 */
  const [loading, setLoading] = useState(true);
  /** saving 表示当前表单是否正在提交保存。 */
  const [saving, setSaving] = useState(false);
  /** errorMessage 保存加载或保存失败的用户可见说明。 */
  const [errorMessage, setErrorMessage] = useState('');
  /** requestController 保存当前弹窗请求，关闭或切换商品时负责取消旧请求。 */
  const requestController = useRef<AbortController | null>(null);
  /** requestGeneration 让取消后仍返回的响应不能覆盖新商品的表单。 */
  const requestGeneration = useRef(0);

  /** isCurrentRequest 判断响应是否仍属于当前商品复合键和当前请求代次。 */
  const isCurrentRequest = useCallback(/* 当前回调验证异步响应是否仍拥有当前商品弹窗。 */ (generation: number, controller: AbortController) => (
    generation === requestGeneration.current && controller === requestController.current && !controller.signal.aborted
  ), []);

  useEffect(/* 当前副作用读取商品提示词并在清理时取消请求。 */ () => {
    /** loadController 是本次商品提示词读取请求的独占取消器。 */
    const loadController = new AbortController();
    requestController.current?.abort();
    requestController.current = loadController;
    /** loadGeneration 是本次读取对应的单调递增代次。 */
    const loadGeneration = ++requestGeneration.current;
    setLoading(true);
    setSaving(false);
    setErrorMessage('');
    setForm(emptyItemAIPromptInput());
    setVariableRows([]);
    void getItemAIPrompt(item.cookie_id, item.item_id, { signal: loadController.signal })
      .then(/* response 是当前商品 AI 提示词读取结果。 */ response => {
        if (!isCurrentRequest(loadGeneration, loadController)) return;
        setForm({ strategy: response.strategy, prompt: response.prompt, custom_variables: response.custom_variables });
        setVariableRows(itemAIVariablesToRows(response.custom_variables));
      })
      .catch(/* error 是商品提示词读取失败或请求取消的原因。 */ error => {
        if (!isCurrentRequest(loadGeneration, loadController)) return;
        setErrorMessage(error instanceof Error ? error.message : '加载商品 AI 提示词失败');
      })
      .finally(/* 当前回调仅在最新读取仍拥有弹窗状态时结束加载。 */ () => {
        if (isCurrentRequest(loadGeneration, loadController)) setLoading(false);
      });
    return /* 清理商品提示词读取请求并使晚到响应失效。 */ () => {
      loadController.abort();
      if (requestController.current === loadController) requestController.current = null;
      requestGeneration.current += 1;
    };
  }, [isCurrentRequest, item.cookie_id, item.item_id]);

  useEffect(/* 当前副作用负责组件卸载时取消仍在执行的商品提示词请求。 */ () => /* 卸载回调使所有未完成响应失去当前弹窗所有权。 */ () => {
    requestController.current?.abort();
    requestGeneration.current += 1;
  }, []);

  /** handleStrategyChange 切换商品提示词与账号默认提示词的合并策略。 */
  const handleStrategyChange = (event: React.ChangeEvent<HTMLSelectElement>) => {
    setForm(/* nextForm 保存用户切换策略后的商品提示词草稿。 */ current => ({ ...current, strategy: event.target.value as ItemAIPromptStrategy }));
  };

  /** handlePromptChange 更新商品提示词正文草稿。 */
  const handlePromptChange = (event: React.ChangeEvent<HTMLTextAreaElement>) => {
    setForm(/* nextForm 保存用户修改后的商品提示词正文。 */ current => ({ ...current, prompt: event.target.value }));
  };

  /** handleInsertVariable 将内置变量追加到提示词末尾，避免覆盖用户已写内容。 */
  const handleInsertVariable = (variable: string) => {
    setForm(/* nextForm 保存插入内置变量后的提示词正文。 */ current => ({ ...current, prompt: `${current.prompt}${current.prompt && !current.prompt.endsWith(' ') ? ' ' : ''}${variable}` }));
  };

  /** handleVariableChange 更新指定自定义变量行，并使用函数式状态避免覆盖相邻输入。 */
  const handleVariableChange = (index: number, field: keyof ItemAIVariableRow, value: string) => {
    setVariableRows(/* nextRows 保存用户编辑变量行后的完整表单列表。 */ current => current.map(/* row 是待判断的变量行；rowIndex 是变量行下标。 */ (row, rowIndex) => rowIndex === index ? { ...row, [field]: value } : row));
  };

  /** handleAddVariable 在变量表末尾追加一个空白编辑行。 */
  const handleAddVariable = () => setVariableRows(current => [...current, createEmptyItemAIVariableRow()]);

  /** handleRemoveVariable 删除用户指定的变量行。 */
  const handleRemoveVariable = (index: number) => setVariableRows(/* nextRows 保存删除目标行后的变量列表。 */ current => current.filter(/* row 是待保留判断的变量行；rowIndex 是变量行下标。 */ (_, rowIndex) => rowIndex !== index));

  /** handleSave 保存当前表单，并只在最新请求仍属于当前商品时反馈结果。 */
  const handleSave = async () => {
    requestController.current?.abort();
    /** saveController 是本次保存请求的独占取消器。 */
    const saveController = new AbortController();
    requestController.current = saveController;
    /** saveGeneration 是本次保存动作的单调递增代次。 */
    const saveGeneration = ++requestGeneration.current;
    setSaving(true);
    setErrorMessage('');
    /** input 是过滤空键后的商品 AI 提示词保存载荷。 */
    const input: ItemAIPromptInput = { ...form, custom_variables: itemAIVariablesToRecord(variableRows) };
    try {
      /** response 是服务端保存后的最新商品提示词配置。 */
      const response = await updateItemAIPrompt(item.cookie_id, item.item_id, input, { signal: saveController.signal });
      if (!isCurrentRequest(saveGeneration, saveController)) return;
      setForm({ strategy: response.strategy, prompt: response.prompt, custom_variables: response.custom_variables });
      setVariableRows(itemAIVariablesToRows(response.custom_variables));
      onClose();
    } catch (error /* error 是商品 AI 提示词保存失败或请求取消的原因。 */) {
      if (!isCurrentRequest(saveGeneration, saveController)) return;
      setErrorMessage(error instanceof Error ? error.message : '保存商品 AI 提示词失败');
    } finally {
      if (isCurrentRequest(saveGeneration, saveController)) setSaving(false);
    }
  };

  return createPortal(
    <div className="modal-overlay-centered" role="dialog" aria-modal="true" aria-labelledby="item-ai-prompt-title">
      <div className="modal-container" style={{ maxWidth: '720px' }}>
        <div className="modal-header flex items-center justify-between">
          <div>
            <h3 id="item-ai-prompt-title" className="text-xl font-extrabold text-gray-900">商品 AI 提示词</h3>
            <p className="mt-1 text-xs text-gray-500">{item.item_title || item.item_id} · ID: {item.item_id}</p>
          </div>
          <button type="button" onClick={onClose} disabled={saving} className="p-2 rounded-xl hover:bg-gray-100 disabled:opacity-50" aria-label="关闭商品 AI 提示词">
            <X className="w-5 h-5 text-gray-500" />
          </button>
        </div>
        <div className="modal-body space-y-5">
          {errorMessage && <div role="alert" className="rounded-xl border border-red-200 bg-red-50 p-3 text-sm text-red-700">{errorMessage}</div>}
          {loading ? <div className="flex items-center justify-center gap-2 py-12 text-sm text-gray-500"><Loader2 className="h-5 w-5 animate-spin" />加载中...</div> : (
            <>
              <div className="space-y-2">
                <label htmlFor="item-ai-prompt-strategy" className="block text-sm font-bold text-gray-700">提示词策略</label>
                <select id="item-ai-prompt-strategy" value={form.strategy} onChange={handleStrategyChange} className="w-full ios-input px-4 py-3 rounded-xl">
                  <option value="inherit">继承账号默认提示词</option>
                  <option value="append">追加到账号默认提示词</option>
                  <option value="override">覆盖账号默认提示词</option>
                </select>
              </div>
              <div className="space-y-2">
                <label htmlFor="item-ai-prompt-content" className="block text-sm font-bold text-gray-700">商品提示词</label>
                <textarea id="item-ai-prompt-content" value={form.prompt} onChange={handlePromptChange} className="w-full ios-input px-4 py-3 rounded-xl h-36 resize-y" placeholder="输入该商品的 AI 回复规则或风格指引..." />
                <div className="flex flex-wrap gap-2">
                  {itemAIBuiltInVariables.map(/* variable 是可插入提示词的内置商品变量。 */ variable => <button key={variable} type="button" onClick={/* 当前回调把用户选择的变量追加到提示词。 */ () => handleInsertVariable(variable)} className="rounded-lg bg-blue-50 px-2.5 py-1.5 text-xs font-bold text-blue-700 hover:bg-blue-100">插入 {variable}</button>)}
                </div>
              </div>
              <div className="space-y-3">
                <div className="flex items-center justify-between gap-3"><div><h4 className="text-sm font-bold text-gray-700">自定义变量</h4><p className="text-xs text-gray-500">变量名不需要输入花括号，提示词中使用 {'{custom.变量名}'}。</p></div><button type="button" onClick={handleAddVariable} className="inline-flex items-center gap-1 rounded-lg bg-gray-100 px-3 py-2 text-xs font-bold text-gray-700 hover:bg-gray-200"><Plus className="h-3.5 w-3.5" />添加变量</button></div>
                {variableRows.map(/* row 是当前自定义变量编辑行；index 是其列表位置。 */ (row, index) => <div key={`${index}-${row.key}`} className="flex items-center gap-2"><input aria-label={`变量名 ${index + 1}`} value={row.key} onChange={/* 当前回调更新用户编辑的变量名称。 */ event => handleVariableChange(index, 'key', event.target.value)} className="ios-input min-w-0 flex-1 rounded-xl px-3 py-2.5 text-sm" placeholder="变量名" /><input aria-label={`变量值 ${index + 1}`} value={row.value} onChange={/* 当前回调更新用户编辑的变量值。 */ event => handleVariableChange(index, 'value', event.target.value)} className="ios-input min-w-0 flex-[2] rounded-xl px-3 py-2.5 text-sm" placeholder="变量值" /><button type="button" onClick={/* 当前回调删除用户指定的变量行。 */ () => handleRemoveVariable(index)} className="rounded-lg p-2 text-red-500 hover:bg-red-50" aria-label={`删除变量 ${index + 1}`}><Trash2 className="h-4 w-4" /></button></div>)}
                {variableRows.length === 0 && <p className="rounded-xl border border-dashed border-gray-200 p-4 text-center text-xs text-gray-400">暂无自定义变量</p>}
              </div>
            </>
          )}
        </div>
        <div className="modal-footer flex gap-3">
          <button type="button" onClick={onClose} disabled={saving} className="flex-1 rounded-xl bg-gray-100 px-6 py-3 font-bold text-gray-700 hover:bg-gray-200 disabled:opacity-50">取消</button>
          <button type="button" onClick={/* 当前回调提交当前商品的提示词表单。 */ () => void handleSave()} disabled={loading || saving} className="ios-btn-primary flex-1 inline-flex items-center justify-center gap-2 rounded-xl px-6 py-3 font-bold disabled:opacity-50"><Save className="h-4 w-4" />{saving ? '保存中...' : '保存'}</button>
        </div>
      </div>
    </div>,
    document.body,
  );
};
