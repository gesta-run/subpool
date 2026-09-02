import { Modal } from '../Modal'
import { Spinner } from '../Spinner'
import type { ProviderAccount, ProviderModel } from '../../types'

export function ModelsDialog({ account, models, loading, error, onClose, onReload }: { account: ProviderAccount; models: ProviderModel[]; loading: boolean; error: string; onClose: () => void; onReload: () => void }) {
  return <Modal title="Supported models" description={`Models currently reported by ${account.display_name}.`} className="modal--models" onClose={onClose}>
    {loading ? <div className="model-list-state" role="status"><Spinner /><span>Loading models…</span></div> : error ? <div className="model-list-state model-list-state--error" role="alert"><div><strong>Models unavailable</strong><span>{error}</span></div><button className="button button--secondary" type="button" onClick={onReload}>Try again</button></div> : models.length === 0 ? <div className="model-list-state"><span>This account did not report any models.</span></div> : <>
      <p className="model-list-summary">{models.length} {models.length === 1 ? 'model' : 'models'} available</p>
      <div className="model-list" role="list" aria-label={`Models supported by ${account.display_name}`}>{models.map((model) => <article className="model-list__item" role="listitem" key={model.id}>
        <div className="model-list__heading"><div><strong>{model.display_name || model.id}</strong>{model.display_name && model.display_name !== model.id ? <code>{model.id}</code> : null}</div>{model.is_default ? <span className="model-badge">Default</span> : null}</div>
        {model.description ? <p>{model.description}</p> : null}
        {model.reasoning_efforts?.length || model.input_modalities?.length ? <div className="model-list__metadata">{model.reasoning_efforts?.length ? <span>Reasoning: {model.reasoning_efforts.join(', ')}</span> : null}{model.input_modalities?.length ? <span>Input: {model.input_modalities.join(', ')}</span> : null}</div> : null}
      </article>)}</div>
    </>}
    <div className="form-actions"><button className="button button--secondary" type="button" onClick={onClose}>Close</button></div>
  </Modal>
}
