import { CreateCampaignForm, useCreateCampaign } from "@/components/CreateCampaignForm";
import { Dialog } from "@/components/ui/Dialog";
import { useI18n } from "@/i18n";

// "New campaign" as a dialog — opened from the Campaigns page header, its empty
// state and the topbar quick switcher. The form used to live inside the
// switcher's 300px popover, where a click anywhere else threw the typing away.
//
// The open dialog is its own component, mounted only while open: the create
// flow lives in it, so every open starts pristine — no name left over, no error
// from the last attempt.
export function NewCampaignDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  return open ? <OpenNewCampaignDialog onClose={() => onOpenChange(false)} /> : null;
}

function OpenNewCampaignDialog({ onClose }: { onClose: () => void }) {
  const { t } = useI18n();
  // Closes itself once the new campaign's selection is written; the flow then
  // toasts and lands on the campaign's Overview (useCreateCampaign).
  const create = useCreateCampaign(onClose);
  return (
    <>
      <Dialog
        open
        onOpenChange={(next) => {
          // While the create is in flight the dialog stays: closing it would
          // take the form away mid-flow and leave the GM guessing whether the
          // campaign exists.
          if (!next && !create.pending) onClose();
        }}
        title={t("components.newCampaign")}
        description={t("campaigns.newCampaignDesc")}
        // A stray click on the backdrop must not throw the form away.
        closeOnOutsideClick={false}
      >
        <CreateCampaignForm
          onSubmit={create.submit}
          pending={create.pending}
          error={create.error}
          autoFocusName
          onCancel={onClose}
        />
      </Dialog>
      {create.dialog}
    </>
  );
}
