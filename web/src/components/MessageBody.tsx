import { memo, useMemo } from "react";

import { renderChat } from "@/lib/markdown";

/**
 * ONE BODY RENDERER, USED BY EVERY LIST OF MESSAGES.
 *
 * It lived inside MessageList until 01M0HP4N06, which the operator raised as
 * "messages in threads dont show attachements". The cause was bigger than
 * attachments: the thread pane is a SECOND renderer of the same events, and its
 * whole message was one span of event.body - no markdown, no mentions, no
 * citation, no cards. Every feature the room grew since the pane was written
 * had to be added to the pane by hand, and none of them had been.
 *
 * So the renderer is a file, and a list that draws messages imports it. A
 * feature added here reaches both. The same argument claude-host made about
 * isMe living twice, one level up.
 *
 * data-body is the id, because that is how the console finds the rendered text
 * of a message: cite and todo both read the browser's selection out of the
 * element with this attribute, and a body drawn without it is a body nobody can
 * quote from.
 */
export const MessageBody = memo(function MessageBody({
  id,
  body,
  mentions,
  user,
  agent,
}: {
  id: string;
  body: string;
  mentions?: string;
  user?: string;
  agent?: string;
}) {
  const html = useMemo(
    () => ({ __html: renderChat(body, mentions, { user, agent }) }),
    [body, mentions, user, agent],
  );
  return (
    <div
      data-body={id}
      className="report-body select-text break-words text-sm"
      // The sanitizer is in lib/markdown, which is why
      // noDangerouslySetInnerHtml is off for this file in biome.json - the
      // rule cannot see through DOMPurify, and the comment cannot sit inside
      // the tag where it fires.
      dangerouslySetInnerHTML={html}
    />
  );
});
